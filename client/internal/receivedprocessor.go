package internal

import (
	"bytes"
	"context"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// receivedProcessor handles the processing of messages received from the Server.
type receivedProcessor struct {
	logger types.Logger

	// Callbacks to call for corresponding messages.
	callbacks types.Callbacks

	// A sender to cooperate with when the received message has an impact on
	// what will be sent later.
	sender Sender

	// Client state storage. This is needed if the Server asks to report the state.
	clientSyncedState *ClientSyncedState

	packagesStateProvider types.PackagesStateProvider
}

func newReceivedProcessor(
	logger types.Logger,
	callbacks types.Callbacks,
	sender Sender,
	clientSyncedState *ClientSyncedState,
	packagesStateProvider types.PackagesStateProvider,
) receivedProcessor {
	return receivedProcessor{
		logger:                logger,
		callbacks:             callbacks,
		sender:                sender,
		clientSyncedState:     clientSyncedState,
		packagesStateProvider: packagesStateProvider,
	}
}

// ProcessReceivedMessage is the entry point into the processing routine. It examines
// the received message and performs any processing necessary based on what fields are set.
// This function will call any relevant callbacks.
func (r *receivedProcessor) ProcessReceivedMessage(ctx context.Context, msg *protobufs.ServerToAgent) {
	if r.callbacks != nil {
		if msg.Command != nil {
			r.rcvCommand(msg.Command)
			// If a command message exists, other messages will be ignored
			return
		}

		scheduled := r.rcvRemoteConfig(ctx, msg.RemoteConfig)

		r.rcvConnectionSettings(ctx, msg.ConnectionSettings)
		scheduled = scheduled || r.rcvPackagesAvailable(ctx, msg.PackagesAvailable)
		r.rcvAgentIdentification(ctx, msg.AgentIdentification)

		scheduled2, err := r.rcvFlags(ctx, msg.Flags)
		if err != nil {
			r.logger.Errorf("cannot processed received flags:%v", err)
		}

		scheduled = scheduled || scheduled2

		if scheduled {
			r.sender.ScheduleSend()
		}
	}

	err := msg.GetErrorResponse()
	if err != nil {
		r.processErrorResponse(err)
	}
}

func (r *receivedProcessor) rcvFlags(
	ctx context.Context,
	flags protobufs.ServerToAgent_Flags,
) (scheduleSend bool, err error) {
	// If the Server asks to report data we fetch it from the client state storage and
	// send to the Server.

	if flags&protobufs.ServerToAgent_ReportAgentDescription != 0 {
		r.sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
			msg.AgentDescription = r.clientSyncedState.AgentDescription()
		})
		scheduleSend = true
	}

	if flags&protobufs.ServerToAgent_ReportRemoteConfigStatus != 0 {
		r.sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
			msg.RemoteConfigStatus = r.clientSyncedState.RemoteConfigStatus()
		})
		scheduleSend = true
	}

	if flags&protobufs.ServerToAgent_ReportPackageStatuses != 0 {
		r.sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
			msg.PackageStatuses = r.clientSyncedState.PackageStatuses()
		})
		scheduleSend = true
	}

	// The logic for EffectiveConfig is similar to the previous 3 messages however
	// the EffectiveConfig is fetched using GetEffectiveConfig instead of
	// from clientSyncedState. We do this to avoid keeping EffectiveConfig in-memory.
	if flags&protobufs.ServerToAgent_ReportEffectiveConfig != 0 {
		cfg, err := r.callbacks.GetEffectiveConfig(ctx)
		if err != nil {
			return false, err
		}
		r.sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
			msg.EffectiveConfig = cfg
		})
		scheduleSend = true
	}

	return scheduleSend, nil
}

func (r *receivedProcessor) rcvRemoteConfig(
	ctx context.Context,
	remoteCfg *protobufs.AgentRemoteConfig,
) (reportStatus bool) {
	if remoteCfg == nil {
		return false
	}

	// Ask the Agent to apply the remote config.
	effective, changed, applyErr := r.callbacks.OnRemoteConfig(ctx, remoteCfg)

	// Begin creating the new status.
	cfgStatus := &protobufs.RemoteConfigStatus{}

	if applyErr != nil {
		// Remote config applying failed.
		cfgStatus.Status = protobufs.RemoteConfigStatus_FAILED
		cfgStatus.ErrorMessage = applyErr.Error()
	} else {
		// Applied successfully.
		cfgStatus.Status = protobufs.RemoteConfigStatus_APPLIED
	}

	// Respond back with the hash of the config received from the Server.
	cfgStatus.LastRemoteConfigHash = remoteCfg.ConfigHash

	// Get the hash of the status before we update it.
	prevStatus := r.clientSyncedState.RemoteConfigStatus()
	var prevCfgStatusHash []byte
	if prevStatus != nil {
		prevCfgStatusHash = prevStatus.Hash
	}

	// Remember the status for the future use.
	_ = r.clientSyncedState.SetRemoteConfigStatus(cfgStatus)

	// Check if the status has changed by comparing the hashes.
	if !bytes.Equal(prevCfgStatusHash, cfgStatus.Hash) {
		// Let the Agent know about the status.
		r.callbacks.SaveRemoteConfigStatus(ctx, cfgStatus)

		// Include the config status in the next message to the Server.
		r.sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
			msg.RemoteConfigStatus = cfgStatus
		})

		reportStatus = true
	}

	// Report the effective configuration if it changed and remote config was applied
	// successfully.
	if changed && applyErr == nil {
		r.sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
			msg.EffectiveConfig = effective
		})
		reportStatus = true
	}
	return reportStatus
}

func (r *receivedProcessor) rcvConnectionSettings(ctx context.Context, settings *protobufs.ConnectionSettingsOffers) {
	if settings == nil {
		return
	}

	if settings.Opamp != nil {
		err := r.callbacks.OnOpampConnectionSettings(ctx, settings.Opamp)
		if err == nil {
			// TODO: verify connection using new settings.
			r.callbacks.OnOpampConnectionSettingsAccepted(settings.Opamp)
		}
	}

	r.rcvOwnTelemetryConnectionSettings(
		ctx,
		settings.OwnMetrics,
		types.OwnMetrics,
		r.callbacks.OnOwnTelemetryConnectionSettings,
	)

	r.rcvOwnTelemetryConnectionSettings(
		ctx,
		settings.OwnTraces,
		types.OwnTraces,
		r.callbacks.OnOwnTelemetryConnectionSettings,
	)

	r.rcvOwnTelemetryConnectionSettings(
		ctx,
		settings.OwnLogs,
		types.OwnLogs,
		r.callbacks.OnOwnTelemetryConnectionSettings,
	)

	for name, s := range settings.OtherConnections {
		r.rcvOtherConnectionSettings(ctx, s, name, r.callbacks.OnOtherConnectionSettings)
	}
}

func (r *receivedProcessor) rcvOwnTelemetryConnectionSettings(
	ctx context.Context,
	settings *protobufs.TelemetryConnectionSettings,
	telemetryType types.OwnTelemetryType,
	callback func(ctx context.Context, telemetryType types.OwnTelemetryType, settings *protobufs.TelemetryConnectionSettings) error,
) {
	if settings != nil {
		callback(ctx, telemetryType, settings)
	}
}

func (r *receivedProcessor) rcvOtherConnectionSettings(
	ctx context.Context,
	settings *protobufs.OtherConnectionSettings,
	name string,
	callback func(ctx context.Context, name string, settings *protobufs.OtherConnectionSettings) error,
) {
	if settings != nil {
		callback(ctx, name, settings)
	}
}

func (r *receivedProcessor) processErrorResponse(body *protobufs.ServerErrorResponse) {
	// TODO: implement this.
	r.logger.Errorf("received an error from server: %s", body.ErrorMessage)
}

func (r *receivedProcessor) rcvPackagesAvailable(ctx context.Context, available *protobufs.PackagesAvailable) bool {
	if available == nil {
		return false
	}

	syncer := NewPackagesSyncer(r.logger, available, r.sender, r.clientSyncedState, r.packagesStateProvider)

	// The OnPackagesAvailable is expected to call syncer.Sync() which will
	// do the syncing process in background.
	err := r.callbacks.OnPackagesAvailable(ctx, available, syncer)
	if err != nil {
		// Could not even start syncing. Just report everything failed.
		statuses := &protobufs.PackageStatuses{
			ServerProvidedAllPackagesHash: available.AllPackagesHash,
			Packages:                      map[string]*protobufs.PackageStatus{},
			// TODO: we need a way to report general errors that occurred when trying
			// install available packages, errors which are not related to a particular
			// package.
		}
		// Until the TODO above is done we will set the same error message for all
		// packages.
		for name, pkg := range available.Packages {
			statuses.Packages[name] = &protobufs.PackageStatus{
				Name:                 name,
				ServerOfferedVersion: pkg.Version,
				ServerOfferedHash:    pkg.Hash,
				Status:               protobufs.PackageStatus_InstallFailed,
				ErrorMessage:         err.Error(),
			}
		}
		_ = r.clientSyncedState.SetPackageStatuses(statuses)
		r.sender.NextMessage().Update(
			func(msg *protobufs.AgentToServer) { msg.PackageStatuses = statuses },
		)
	}

	return true
}

func (r *receivedProcessor) rcvAgentIdentification(ctx context.Context, agentId *protobufs.AgentIdentification) {
	if agentId == nil {
		return
	}

	if agentId.NewInstanceUid == "" {
		r.logger.Debugf("Empty instance uid is not allowed")
		return
	}

	err := r.callbacks.OnAgentIdentification(ctx, agentId)
	if err != nil {
		r.logger.Errorf("Error while updating agent identification: %v", err)
		return
	}

	err = r.sender.SetInstanceUid(agentId.NewInstanceUid)
	if err != nil {
		r.logger.Errorf("Error while setting instance uid: %v, err")
	}
}

func (r *receivedProcessor) rcvCommand(command *protobufs.ServerToAgentCommand) {
	if command != nil {
		r.callbacks.OnCommand(command)
	}
}
