package types

import (
	"context"
	"net"

	"github.com/open-telemetry/opamp-go/protobufs"
)

const (
	// ConnectionTypeHTTP is the type of a plain HTTP connection.
	ConnectionTypeHTTP ConnectionType = "http"
	// ConnectionTypeWebSocket is the type of a WebSocket connection.
	ConnectionTypeWebSocket ConnectionType = "websocket"
)

// ConnectionType is the type of a connection.
// It can be one of ConnectionTypeHTTP or ConnectionTypeWebSocket.
type ConnectionType string

// Connection represents one OpAMP connection.
// The implementation MUST be a comparable type so that it can be used as a map key.
type Connection interface {
	// Connection returns the underlying net.Conn
	Connection() net.Conn

	// Type returns the type of the connection.
	// The type is one of http, websocket, or unknown.
	Type() ConnectionType

	// Send a message. Should not be called concurrently for the same Connection instance.
	// Can be called only for WebSocket connections. Will return an error for plain HTTP
	// connections.
	// Blocks until the message is sent.
	// Should return as soon as possible if the ctx is cancelled.
	Send(ctx context.Context, message *protobufs.ServerToAgent) error

	// Disconnect closes the network connection.
	// Any blocked Read or Write operations will be unblocked and return errors.
	Disconnect() error
}
