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

// ErrSendBeforeNegotiated is returned from Send when the server has
// a PayloadSigner configured but no AgentToServer has been processed
// yet on this connection. In that window the server cannot know
// whether the agent will declare RequiresPayloadTrustVerification, so
// emitting a message risks bypassing attestation. Push outbound
// messages from OnMessage (or any callback that runs after the first
// agent message), not from OnConnected.
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

	// requiresNegotiation is fixed at construction. When true the
	// server has a PayloadSigner configured and Send is rejected until
	// negotiated flips to true. When false (no server-side signer),
	// Send is always permitted; the Server sends the standard
	// ServerToAgent wire format.
	requiresNegotiation bool

	// negotiated flips to true after the connection's first
	// AgentToServer has been processed by handleWSConnection. After
	// that point the server has had its chance to decide whether to
	// enable signing based on the agent's capability bits, so Send is
	// safe to call.
	negotiated atomic.Bool

	// signing, when loaded as non-nil, indicates that this connection
	// has negotiated payload trust verification with the Agent.
	// Outbound ServerToAgent messages are wrapped in a
	// SignedServerToAgent envelope and the first send carries the
	// trust chain. atomic.Pointer because enableSigning and Send may
	// be called from different goroutines (Send is part of the public
	// Connection callback API and may be invoked by user code).
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

// enableSigning marks this connection as one that has negotiated
// payload trust verification. Outbound Send calls will wrap their
// ServerToAgent argument in a SignedServerToAgent envelope using the
// supplied state.
func (c *wsConnection) enableSigning(state *connectionSigningState) {
	c.signing.Store(state)
}

// signingEnabled reports whether this connection has negotiated
// payload trust verification.
func (c *wsConnection) signingEnabled() bool {
	return c.signing.Load() != nil
}

// markNegotiated records that the connection has processed its first
// AgentToServer message. After this point Send is no longer blocked
// by the pre-negotiation guard.
func (c *wsConnection) markNegotiated() {
	c.negotiated.Store(true)
}

// isNegotiated reports whether the connection has processed its
// first AgentToServer message.
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
