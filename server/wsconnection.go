package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"

	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// ErrSendBeforeNegotiated is returned from Send when the server has a
// PayloadSigner but has not yet processed the connection's first
// AgentToServer, so it cannot know whether the Agent requires attestation.
// Send from OnMessage rather than OnConnected.
var ErrSendBeforeNegotiated = errors.New(
	"server: Send called before Message Attestation negotiation completed; " +
		"push outbound messages from OnMessage rather than OnConnected",
)

// wsConnection represents a persistent OpAMP connection over a WebSocket.
type wsConnection struct {
	// The websocket library does not allow multiple concurrent write operations,
	// so ensure that we only have a single operation in progress at a time.
	// For more: https://pkg.go.dev/github.com/gorilla/websocket#hdr-Concurrency
	connMutex sync.Mutex
	wsConn    *websocket.Conn
	closed    atomic.Bool

	maxMessageSize int64

	// requiresNegotiation (server has a PayloadSigner) blocks Send until
	// negotiated, i.e. until the first AgentToServer has been processed.
	requiresNegotiation bool
	negotiated          atomic.Bool

	// signing is non-nil once the connection has negotiated attestation;
	// Send then signs every non-heartbeat message. Atomic because user code
	// may call Send from other goroutines.
	signing atomic.Pointer[connectionSigningState]
}

var _ types.Connection = (*wsConnection)(nil)

func newWSConnection(wsConn *websocket.Conn, maxMessageSize int64, requiresNegotiation bool) *wsConnection {
	return &wsConnection{
		wsConn:              wsConn,
		maxMessageSize:      maxMessageSize,
		requiresNegotiation: requiresNegotiation,
	}
}

// enableSigning makes Send sign outbound messages with state.
func (c *wsConnection) enableSigning(state *connectionSigningState) {
	c.signing.Store(state)
}

// markNegotiated records that the first AgentToServer has been processed,
// unblocking Send.
func (c *wsConnection) markNegotiated() {
	c.negotiated.Store(true)
}

// isNegotiated reports whether markNegotiated has been called.
func (c *wsConnection) isNegotiated() bool {
	return c.negotiated.Load()
}

func (c *wsConnection) Connection() net.Conn {
	return c.wsConn.UnderlyingConn()
}

func (c *wsConnection) Send(ctx context.Context, message *protobufs.ServerToAgent) error {
	if c.requiresNegotiation && !c.negotiated.Load() {
		return ErrSendBeforeNegotiated
	}

	c.connMutex.Lock()
	defer c.connMutex.Unlock()

	if state := c.signing.Load(); state != nil {
		// A heartbeat response (instance_uid only) MAY be sent unsigned.
		if protobufs.IsHeartbeatServerToAgent(message) {
			return internal.WriteWSMessage(c.wsConn, message, c.maxMessageSize)
		}
		env, err := state.signOutgoing(ctx, message)
		if err != nil {
			return err
		}
		return internal.WriteWSMessage(c.wsConn, env, c.maxMessageSize)
	}

	return internal.WriteWSMessage(c.wsConn, message, c.maxMessageSize)
}

func (c *wsConnection) Disconnect() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	return c.wsConn.Close()
}
