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
	sender sender
}

type sender interface {
	// NextMessage provides access to modify the next message that will be sent.
	NextMessage() *nextMessage

	// ScheduleSend signals the sender to send a pending next message.
	ScheduleSend()

	// SetInstanceUid sets a new instanceUid to be used when next message is being sent.
	SetInstanceUid(instanceUid string) error
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

		reportStatus := r.rcvRemoteConfig(ctx, msg.RemoteConfig, msg.Flags)

		r.rcvConnectionSettings(ctx, msg.ConnectionSettings)
		r.rcvAddonsAvailable(msg.AddonsAvailable)
		r.rcvAgentIdentification(ctx, msg.AgentIdentification)

		if reportStatus {
			r.sender.ScheduleSend()
		}
	}

	err := msg.GetErrorResponse()
	if err != nil {
		r.processErrorResponse(err)
	}
}

func (r *receivedProcessor) rcvRemoteConfig(
	ctx context.Context,
	config *protobufs.AgentRemoteConfig,
	flags protobufs.ServerToAgent_Flags,
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

	// Report the effective configuration if it changed or if the Server explicitly
	// asked us to report it.
	reportEffective := changed || (flags&protobufs.ServerToAgent_ReportEffectiveConfig != 0)

	if reportEffective {
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

func (r *receivedProcessor) rcvAddonsAvailable(addons *protobufs.AddonsAvailable) {
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
