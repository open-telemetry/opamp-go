package internal

import (
	"context"
	"fmt"

	"github.com/gorilla/websocket"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// wsReceiver implements the WebSocket client's receiving portion of OpAMP protocol.
type wsReceiver struct {
	conn      *websocket.Conn
	logger    types.Logger
	sender    *WSSender
	callbacks types.Callbacks
	processor receivedProcessor
}

// NewWSReceiver creates a new Receiver that uses WebSocket to receive
// messages from the server.
func NewWSReceiver(
	logger types.Logger,
	callbacks types.Callbacks,
	conn *websocket.Conn,
	sender *WSSender,
	clientSyncedState *ClientSyncedState,
	packagesStateProvider types.PackagesStateProvider,
	capabilities protobufs.AgentCapabilities,
) *wsReceiver {
	w := &wsReceiver{
		conn:      conn,
		logger:    logger,
		sender:    sender,
		callbacks: callbacks,
		processor: newReceivedProcessor(logger, callbacks, sender, clientSyncedState, packagesStateProvider, capabilities),
	}

	return w
}

// ReceiverLoop runs the receiver loop. To stop the receiver cancel the context.
func (r *wsReceiver) ReceiverLoop(ctx context.Context) {
	type receivedMessage struct {
		message *protobufs.ServerToAgent
		err     error
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			result := make(chan receivedMessage, 1)

			go func() {
				var message protobufs.ServerToAgent
				err := r.receiveMessage(&message)
				result <- receivedMessage{&message, err}
			}()

			select {
			case <-ctx.Done():
				return
			case res := <-result:
				if res.err != nil {
					if !websocket.IsCloseError(res.err, websocket.CloseNormalClosure) {
						r.logger.Errorf(ctx, "Unexpected error while receiving: %v", res.err)
					}
					return
				}
				r.processor.ProcessReceivedMessage(ctx, res.message)
			}
		}
	}
}

func (r *wsReceiver) receiveMessage(msg *protobufs.ServerToAgent) error {
	_, bytes, err := r.conn.ReadMessage()
	if err != nil {
		return err
	}
	err = internal.DecodeWSMessage(bytes, msg)
	if err != nil {
		return fmt.Errorf("cannot decode received message: %w", err)
	}
	return err
}
