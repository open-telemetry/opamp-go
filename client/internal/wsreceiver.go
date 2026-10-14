package internal

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

// wsReceiver implements the WebSocket client's receiving portion of OpAMP protocol.
type wsReceiver struct {
	conn      *websocket.Conn
	logger    types.Logger
	sender    *WSSender
	callbacks types.Callbacks
	processor receivedProcessor

	// attestation, when non-nil, verifies inbound messages.
	attestation *attestationState

	// Indicates that the receiver has fully stopped.
	stopped chan struct{}

	// Why the loop exited; set before stopped is closed, so read only after
	// <-IsStopped(). connectionError is an abnormal close with attestation
	// enabled, which usually means the server cannot sign.
	attestationFailure bool
	connectionError    bool
}

// NewWSReceiver creates a new Receiver that uses WebSocket to receive
// messages from the server. With a payloadVerifier or tofuEnroller, inbound
// messages are verified and a failure terminates the connection.
func NewWSReceiver(
	logger types.Logger,
	callbacks types.Callbacks,
	conn *websocket.Conn,
	sender *WSSender,
	clientSyncedState *ClientSyncedState,
	packagesStateProvider types.PackagesStateProvider,
	packageSyncMutex *sync.Mutex,
	reporterInterval time.Duration,
	payloadVerifier signing.Verifier,
	serverURL string,
	tofuEnroller signing.TOFUEnroller,
) *wsReceiver {
	w := &wsReceiver{
		conn:      conn,
		logger:    logger,
		sender:    sender,
		callbacks: callbacks,
		processor: newReceivedProcessor(logger, callbacks, sender, clientSyncedState, packagesStateProvider, packageSyncMutex, reporterInterval),
		stopped:   make(chan struct{}),
	}
	if payloadVerifier != nil || tofuEnroller != nil {
		var serverName string
		if parsed, err := url.Parse(serverURL); err != nil {
			// An empty serverName fails closed in ValidateChain.
			logger.Errorf(context.Background(), "Cannot parse server URL %q for SAN verification: %v", serverURL, err)
		} else {
			serverName = parsed.Hostname()
		}
		w.attestation = newAttestationState(payloadVerifier, serverName, tofuEnroller)
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

// WasAttestationFailure reports whether the receiver stopped on an
// attestation failure. Only valid after <-IsStopped() returns.
func (r *wsReceiver) WasAttestationFailure() bool {
	return r.attestationFailure
}

// WasAttested reports whether any signed message verified on this
// connection. Only valid after <-IsStopped() returns.
func (r *wsReceiver) WasAttested() bool {
	return r.attestation != nil && r.attestation.HasVerified()
}

// WasConnectionError reports whether the receiver stopped on an abnormal
// close with attestation enabled. Only valid after <-IsStopped() returns.
func (r *wsReceiver) WasConnectionError() bool {
	return r.connectionError
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
				err := r.receiveMessage(ctx, &message)
				result <- receivedMessage{&message, err}
			}()

			select {
			case <-ctx.Done():
				return
			case res := <-result:
				if res.err != nil {
					if isAttestationFailure(res.err) {
						// The spec requires terminating the connection.
						// Close now so the sender cannot write more
						// messages to the untrusted server meanwhile.
						r.logger.Errorf(ctx, "Payload trust verification failed; terminating connection: %v", res.err)
						if r.conn != nil {
							_ = r.conn.Close()
						}
						r.attestationFailure = true
						return
					}
					if !websocket.IsCloseError(res.err, websocket.CloseNormalClosure) {
						r.logger.Errorf(ctx, "Unexpected error while receiving: %v", res.err)
						// Likely a server that cannot sign; back off.
						if r.attestation != nil {
							r.connectionError = true
						}
					}
					return
				}
				r.processor.ProcessReceivedMessage(ctx, res.message)
			}
		}
	}
}

func (r *wsReceiver) receiveMessage(ctx context.Context, msg *protobufs.ServerToAgent) error {
	mt, bytes, err := r.conn.ReadMessage()
	if err != nil {
		return err
	}
	if mt != websocket.BinaryMessage {
		return fmt.Errorf("unsupported message type: %v", mt)
	}
	protoBytes, err := internal.StripWSMessageHeader(bytes)
	if err != nil {
		return fmt.Errorf("cannot decode received message: %w", err)
	}
	if err := unwrapServerToAgent(ctx, r.attestation, protoBytes, msg); err != nil {
		return fmt.Errorf("cannot decode received message: %w", err)
	}
	return nil
}
