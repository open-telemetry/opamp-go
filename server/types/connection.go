package types

import (
	"context"

	"github.com/open-telemetry/opamp-go/protobufs"
)

// Connection represents one OpAMP WebSocket connections.
// The implementation MUST be a comparable type so that it can be used as a map key.
type Connection interface {
	// Send a message. Should not be called concurrently for the same Connection instance.
	// Blocks until the message is sent.
	// Should return as soon as possible if the ctx is cancelled.
	Send(ctx context.Context, message *protobufs.ServerToAgent) error
}
