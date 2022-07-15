package internal

import (
	"context"
	"errors"

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

		scheduled, err := r.rcvFlags(ctx, msg.Flags)
		if err != nil {
			r.logger.Errorf("cannot processed received flags:%v", err)
		}

		msgData := &types.MessageData{
			RemoteConfig: msg.RemoteConfig,
		}

		if msg.ConnectionSettings != nil {
			msgData.OwnMetricsConnSettings = msg.ConnectionSettings.OwnMetrics
			msgData.OwnTracesConnSettings = msg.ConnectionSettings.OwnTraces
			msgData.OwnLogsConnSettings = msg.ConnectionSettings.OwnLogs
			msgData.OtherConnSettings = msg.ConnectionSettings.OtherConnections
		}

		if msg.PackagesAvailable != nil {
			msgData.PackagesAvailable = msg.PackagesAvailable
			msgData.PackageSyncer = NewPackagesSyncer(
				r.logger,
				msgData.PackagesAvailable,
				r.sender,
				r.clientSyncedState,
				r.packagesStateProvider,
			)
		}

		if msg.AgentIdentification != nil {
			err := r.rcvAgentIdentification(msg.AgentIdentification)
			if err == nil {
				msgData.AgentIdentification = msg.AgentIdentification
			}
		}

		r.callbacks.OnMessage(ctx, msgData)

		r.rcvOpampConnectionSettings(ctx, msg.ConnectionSettings)

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

	if flags&protobufs.ServerToAgent_ReportFullState != 0 {
		cfg, err := r.callbacks.GetEffectiveConfig(ctx)
		if err != nil {
			r.logger.Errorf("Cannot GetEffectiveConfig: %v", err)
			cfg = nil
		}

		r.sender.NextMessage().Update(
			func(msg *protobufs.AgentToServer) {
				msg.AgentDescription = r.clientSyncedState.AgentDescription()
				msg.Health = r.clientSyncedState.Health()
				msg.RemoteConfigStatus = r.clientSyncedState.RemoteConfigStatus()
				msg.PackageStatuses = r.clientSyncedState.PackageStatuses()
				msg.Flags |= protobufs.AgentToServer_StatusIsFullySet

				// The logic for EffectiveConfig is similar to the previous 4 sub-messages however
				// the EffectiveConfig is fetched using GetEffectiveConfig instead of
				// from clientSyncedState. We do this to avoid keeping EffectiveConfig in-memory.
				msg.EffectiveConfig = cfg
			},
		)
		scheduleSend = true
	}

	return scheduleSend, nil
}

func (r *receivedProcessor) rcvOpampConnectionSettings(ctx context.Context, settings *protobufs.ConnectionSettingsOffers) {
	if settings == nil || settings.Opamp == nil {
		return
	}

	err := r.callbacks.OnOpampConnectionSettings(ctx, settings.Opamp)
	if err == nil {
		// TODO: verify connection using new settings.
		r.callbacks.OnOpampConnectionSettingsAccepted(settings.Opamp)
	}
}

func (r *receivedProcessor) processErrorResponse(body *protobufs.ServerErrorResponse) {
	// TODO: implement this.
	r.logger.Errorf("received an error from server: %s", body.ErrorMessage)
}

func (r *receivedProcessor) rcvAgentIdentification(agentId *protobufs.AgentIdentification) error {
	if agentId.NewInstanceUid == "" {
		err := errors.New("empty instance uid is not allowed")
		r.logger.Debugf(err.Error())
		return err
	}

	err := r.sender.SetInstanceUid(agentId.NewInstanceUid)
	if err != nil {
		r.logger.Errorf("Error while setting instance uid: %v, err")
		return err
	}

	return nil
}

func (r *receivedProcessor) rcvCommand(command *protobufs.ServerToAgentCommand) {
	if command != nil {
		r.callbacks.OnCommand(command)
	}
}
