package internal

import (
	"context"
	"fmt"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// wsReceiver implements the WebSocket client's receiving portion of OpAMP protocol.
type wsReceiver struct {
	conn      internal.WebsocketConn
	logger    types.Logger
	sender    *WSSender
	callbacks types.Callbacks
	processor rcvProcessor

	// Indicates that the receiver has fully stopped.
	stopped chan struct{}

	metrics *types.ClientMetrics
}

// NewWSReceiver creates a new Receiver that uses WebSocket to receive
// messages from the server.
func NewWSReceiver(
	logger types.Logger,
	callbacks types.Callbacks,
	conn internal.WebsocketConn,
	sender *WSSender,
	clientSyncedState *ClientSyncedState,
	packagesStateProvider types.PackagesStateProvider,
	capabilities protobufs.AgentCapabilities,
	packageSyncMutex *sync.Mutex,
	metrics *types.ClientMetrics,
) *wsReceiver {
	w := &wsReceiver{
		conn:      conn,
		logger:    logger,
		sender:    sender,
		callbacks: callbacks,
		processor: newReceivedProcessor(logger, callbacks, sender, clientSyncedState, packagesStateProvider, capabilities, packageSyncMutex),
		stopped:   make(chan struct{}),
		metrics:   metrics,
	}

	return w
}

// Start starts the receiver loop.
func (r *wsReceiver) Start(ctx context.Context) {
	go r.ReceiverLoop(ctx)
}

// IsStopped returns a channel that's closed when the receiver is stopped.
func (r *wsReceiver) IsStopped() <-chan struct{} {
	return r.stopped
}

// ReceiverLoop runs the receiver loop.
// To stop the receiver cancel the context and close the websocket connection
func (r *wsReceiver) ReceiverLoop(ctx context.Context) {
	type receivedMessage struct {
		message *protobufs.ServerToAgent
		err     error
	}

	defer func() { close(r.stopped) }()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			result := make(chan receivedMessage, 1)

			// To stop this goroutine, close the websocket connection
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

func rxMessageAttrs(msg *protobufs.ServerToAgent) types.MessageAttrs {
	attrs := types.MessageAttrs(types.RxMessageAttr | types.ServerToAgentMessageAttr)
	if msg.ErrorResponse != nil {
		attrs.Set(types.ErrorMessageAttr)
	}
	return attrs
}

func (r *wsReceiver) receiveMessage(msg *protobufs.ServerToAgent) error {
	_, bytes, err := r.conn.ReadMessage()
	r.metrics.RxBytes.Add(int64(len(bytes)))
	if err != nil {
		r.metrics.RxErrors.Add(1)
		return err
	}
	r.metrics.RxMessages.Add(1)
	err = internal.DecodeWSMessage(bytes, msg)
	if err != nil {
		r.metrics.RxErrors.Add(1)
		return fmt.Errorf("cannot decode received message: %w", err)
	}
	r.metrics.RxMessageInfo.Insert(types.RxMessageInfo{
		InstanceUID:  msg.InstanceUid,
		Capabilities: msg.Capabilities,
		Attrs:        wsMessageAttrs(rxMessageAttrs(msg)),
	})
	return nil
}
