package internal

import (
	"context"
	"fmt"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
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
		processor: newReceivedProcessor(logger, callbacks, sender, &sender.SenderCommon, clientSyncedState, packagesStateProvider, capabilities),
	}

	return w
}

// ReceiverLoop runs the receiver loop. To stop the receiver cancel the context.
func (r *wsReceiver) ReceiverLoop(ctx context.Context) {
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
			r.processor.ProcessReceivedMessage(runContext, &message)
		}
	}

	cancelFunc()
}

func (r *wsReceiver) receiveMessage(msg *protobufs.ServerToAgent) error {
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
