package internal

import (
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
}

func newReceivedProcessor(
	logger types.Logger,
	callbacks types.Callbacks,
	sender Sender,
	clientSyncedState *ClientSyncedState,
) receivedProcessor {
	return receivedProcessor{
		logger:            logger,
		callbacks:         callbacks,
		sender:            sender,
		clientSyncedState: clientSyncedState,
	}
}

// ProcessReceivedMessage is the entry point into the processing routine. It examines
// the received message and performs any processing necessary based on what fields are set.
// This function will call any relevant callbacks.
func (r *receivedProcessor) ProcessReceivedMessage(ctx context.Context, msg *protobufs.ServerToAgent) {
	if r.callbacks != nil {
		// If a command message exists, other messages will be ignored
		if msg.Command != nil {
			r.rcvCommand(msg.Command)
			return
		}

		scheduled := r.rcvRemoteConfig(ctx, msg.RemoteConfig)

		r.rcvConnectionSettings(ctx, msg.ConnectionSettings)
		r.rcvPackagesAvailable(msg.PackagesAvailable)
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
		r.sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
			statusReport.AgentDescription = r.clientSyncedState.AgentDescription()
		})
		scheduleSend = true
	}

	if flags&protobufs.ServerToAgent_ReportRemoteConfigStatus != 0 {
		r.sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
			statusReport.RemoteConfigStatus = r.clientSyncedState.RemoteConfigStatus()
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
		r.sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
			statusReport.EffectiveConfig = cfg
		})
		scheduleSend = true
	}

	return scheduleSend, nil
}

func (r *receivedProcessor) rcvRemoteConfig(
	ctx context.Context,
	config *protobufs.AgentRemoteConfig,
) (reportStatus bool) {
	effective, changed, err := r.callbacks.OnRemoteConfig(ctx, config)
	if err != nil {
		// Return a status message with status failed.
		r.sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
			statusReport.RemoteConfigStatus = &protobufs.RemoteConfigStatus{
				Status:       protobufs.RemoteConfigStatus_Failed,
				ErrorMessage: err.Error(),
			}
		})
		return true
	}

	// Report the effective configuration if it changed.
	if changed {
		r.sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
			statusReport.EffectiveConfig = effective
		})
		return true
	}
	return false
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
	settings *protobufs.ConnectionSettings,
	telemetryType types.OwnTelemetryType,
	callback func(ctx context.Context, telemetryType types.OwnTelemetryType, settings *protobufs.ConnectionSettings) error,
) {
	if settings != nil {
		callback(ctx, telemetryType, settings)
	}
}

func (r *receivedProcessor) rcvOtherConnectionSettings(
	ctx context.Context,
	settings *protobufs.ConnectionSettings,
	name string,
	callback func(ctx context.Context, name string, settings *protobufs.ConnectionSettings) error,
) {
	if settings != nil {
		callback(ctx, name, settings)
	}
}

func (r *receivedProcessor) processErrorResponse(body *protobufs.ServerErrorResponse) {
	// TODO: implement this.
	r.logger.Errorf("received an error from server: %s", body.ErrorMessage)
}

func (r *receivedProcessor) rcvPackagesAvailable(packages *protobufs.PackagesAvailable) {
	// TODO: implement this.
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
