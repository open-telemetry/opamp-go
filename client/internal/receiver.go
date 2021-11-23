package internal

import (
	"context"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/protobufs"
)

// Receiver implements the client's receiving portion of OpAMP protocol.
type Receiver struct {
	conn       *websocket.Conn
	logger     types.Logger
	sender     *Sender
	runContext context.Context
	callbacks  types.Callbacks
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
	r.runContext = runContext

out:
	for {
		var message protobufs.ServerToAgent
		if err := r.receiveMessage(&message); err != nil {
			if ctx.Err() == nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				r.logger.Errorf("Unexpected error while receiving: %v", err)
			}
			break out
		} else {
			r.processReceivedMessage(&message)
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
		r.logger.Errorf("Cannot decode received message: %v", err)
	}
	return err
}

func (r *Receiver) processReceivedMessage(p *protobufs.ServerToAgent) {
	data := p.GetDataForAgent()
	if data != nil {
		r.rcvDataForAgent(data)
		return
	}

	err := p.GetErrorResponse()
	if err != nil {
		r.processErrorResponse(err)
	}
}

func (r *Receiver) rcvDataForAgent(data *protobufs.DataForAgent) {
	if r.callbacks != nil {
		reportStatus := false

		reportStatus = r.rcvRemoteConfig(data.RemoteConfig) || reportStatus
		reportStatus = r.rcvConnectionSettings(data.ConnectionSettings) || reportStatus
		r.rcvAddonsAvailable(data.AddonsAvailable)

		if reportStatus {
			r.sender.ScheduleSend()
		}
	}
}

func (r *Receiver) rcvRemoteConfig(config *protobufs.AgentRemoteConfig) (reportStatus bool) {
	effective, err := r.callbacks.OnRemoteConfig(r.runContext, config)
	if err == nil {
		r.sender.UpdateStatus(func(statusReport *protobufs.StatusReport) {
			statusReport.EffectiveConfig = effective
		})
		if effective != nil {
			return true
		}
	}
	return false
}

func (r *Receiver) rcvConnectionSettings(settings *protobufs.ConnectionSettingsOffers) (reportStatus bool) {
	if settings == nil {
		return false
	}

	if settings.Opamp != nil {
		err := r.callbacks.OnOpampConnectionSettings(r.runContext, settings.Opamp)
		if err == nil {
			// TODO: verify connection using new settings.
			r.callbacks.OnOpampConnectionSettingsAccepted(settings.Opamp)
		}
	}

	r.rcvOwnTelemetryConnectionSettings(
		settings.OwnMetrics,
		types.OwnMetrics,
		r.callbacks.OnOwnTelemetryConnectionSettings,
	)

	r.rcvOwnTelemetryConnectionSettings(
		settings.OwnTraces,
		types.OwnTraces,
		r.callbacks.OnOwnTelemetryConnectionSettings,
	)

	r.rcvOwnTelemetryConnectionSettings(
		settings.OwnLogs,
		types.OwnLogs,
		r.callbacks.OnOwnTelemetryConnectionSettings,
	)

	for name, s := range settings.OtherConnections {
		r.rcvOtherConnectionSettings(s, name, r.callbacks.OnOtherConnectionSettings)
	}

	return false
}

func (r *Receiver) rcvOwnTelemetryConnectionSettings(
	settings *protobufs.ConnectionSettings,
	telemetryType types.OwnTelemetryType,
	callback func(ctx context.Context, telemetryType types.OwnTelemetryType, settings *protobufs.ConnectionSettings) error,
) {
	if settings != nil {
		callback(r.runContext, telemetryType, settings)
	}
}

func (r *Receiver) rcvOtherConnectionSettings(
	settings *protobufs.ConnectionSettings,
	name string,
	callback func(ctx context.Context, name string, settings *protobufs.ConnectionSettings) error,
) {
	if settings != nil {
		callback(r.runContext, name, settings)
	}
}

func (r *Receiver) processErrorResponse(body *protobufs.ServerErrorResponse) {
	// TODO: implement this.
}

func (r *Receiver) rcvAddonsAvailable(addons *protobufs.AddonsAvailable) {
	// TODO: implement this.
}
