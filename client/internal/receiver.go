package internal

import (
	"context"
	"fmt"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// Receiver implements the client's receiving portion of OpAMP protocol.
type Receiver struct {
	conn      *websocket.Conn
	logger    types.Logger
	sender    *Sender
	callbacks types.Callbacks
}

func NewReceiver(logger types.Logger, callbacks types.Callbacks, conn *websocket.Conn, sender *Sender) *Receiver {
	return &Receiver{
		conn:      conn,
		logger:    logger,
		sender:    sender,
		callbacks: callbacks,
	}
}

func (r *Receiver) ReceiverLoop(ctx context.Context) {
	runContext, cancelFunc := context.WithCancel(ctx)

out:
	for {
		var message protobufs.ServerToAgent
		if err := r.receiveMessage(&message); err != nil {
			if ctx.Err() == nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				r.logger.Errorf("Unexpected error while receiving: %v", err)
			}
			break out
		} else {
			r.processReceivedMessage(runContext, &message)
		}
	}

	cancelFunc()
}

func (r *Receiver) receiveMessage(msg *protobufs.ServerToAgent) error {
	_, bytes, err := r.conn.ReadMessage()
	if err != nil {
		return err
	}
	err = proto.Unmarshal(bytes, msg)
	if err != nil {
		return fmt.Errorf("cannot decode received message: %w", err)
	}
	return err
}

func (r *Receiver) processReceivedMessage(ctx context.Context, msg *protobufs.ServerToAgent) {
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

func (r *Receiver) rcvRemoteConfig(
	ctx context.Context,
	config *protobufs.AgentRemoteConfig,
	flags protobufs.ServerToAgent_Flags,
) (reportStatus bool) {
	effective, changed, err := r.callbacks.OnRemoteConfig(ctx, config)
	if err != nil {
		// Return a status message with status failed.
		r.sender.UpdateNextStatus(func(statusReport *protobufs.StatusReport) {
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
		r.sender.UpdateNextStatus(func(statusReport *protobufs.StatusReport) {
			statusReport.EffectiveConfig = effective
		})
		return true
	}
	return false
}

func (r *Receiver) rcvConnectionSettings(ctx context.Context, settings *protobufs.ConnectionSettingsOffers) {
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

func (r *Receiver) rcvOwnTelemetryConnectionSettings(
	ctx context.Context,
	settings *protobufs.ConnectionSettings,
	telemetryType types.OwnTelemetryType,
	callback func(ctx context.Context, telemetryType types.OwnTelemetryType, settings *protobufs.ConnectionSettings) error,
) {
	if settings != nil {
		callback(ctx, telemetryType, settings)
	}
}

func (r *Receiver) rcvOtherConnectionSettings(
	ctx context.Context,
	settings *protobufs.ConnectionSettings,
	name string,
	callback func(ctx context.Context, name string, settings *protobufs.ConnectionSettings) error,
) {
	if settings != nil {
		callback(ctx, name, settings)
	}
}

func (r *Receiver) processErrorResponse(body *protobufs.ServerErrorResponse) {
	// TODO: implement this.
}

func (r *Receiver) rcvAddonsAvailable(addons *protobufs.AddonsAvailable) {
	// TODO: implement this.
}

func (r *Receiver) rcvAgentIdentification(ctx context.Context, agentId *protobufs.AgentIdentification) {
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

func (r *Receiver) rcvCommand(command *protobufs.ServerToAgentCommand) {
	if command != nil {
		r.callbacks.OnCommand(command)
	}
}
