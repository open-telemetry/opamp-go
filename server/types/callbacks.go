package types

import (
	"context"
	"net/http"

	"github.com/open-telemetry/opamp-go/protobufs"
)

// ConnectionResponse is the return type of the OnConnecting callback.
type ConnectionResponse struct {
	Accept              bool
	HTTPStatusCode      int
	HTTPResponseHeader  map[string]string
	ConnectionCallbacks ConnectionCallbacks
}

type Callbacks struct {
	// OnConnecting is called when there is a new incoming connection.
	// The handler can examine the request and either accept or reject the connection.
	// To accept:
	//   Return ConnectionResponse with Accept=true. ConnectionCallbacks MUST be set to an
	//   implementation of the ConnectionCallbacks interface to handle the connection.
	//   HTTPStatusCode and HTTPResponseHeader are ignored.
	//
	// To reject:
	//   Return ConnectionResponse with Accept=false. HTTPStatusCode MUST be set to
	//   non-zero value to indicate the rejection reason (typically 401, 429 or 503).
	//   HTTPResponseHeader may be optionally set (e.g. "Retry-After: 30").
	//   ConnectionCallbacks is ignored.
	OnConnecting func(request *http.Request) ConnectionResponse
}

func (c *Callbacks) SetDefaults() {
	if c.OnConnecting == nil {
		c.OnConnecting = func(r *http.Request) ConnectionResponse {
			return ConnectionResponse{Accept: true}
		}
	}
}

// ConnectionCallbacks receives callbacks for a specific connection. An implementation of
// this interface MUST be set on the ConnectionResponse returned by the OnConnecting
// callback if Accept=true. The implementation can be shared by all connections or can be
// unique for each connection.
type ConnectionCallbacks struct {
	// The following callbacks will never be called concurrently for the same
	// connection. They may be called concurrently for different connections.

	// OnConnected is called when an incoming OpAMP connection is successfully
	// established after OnConnecting() returns.
	OnConnected func(ctx context.Context, conn Connection)

	// OnMessage is called when a message is received from the connection. Can happen
	// only after OnConnected(). Must return a ServerToAgent message that will be sent
	// as a response to the Agent.
	// For plain HTTP requests once OnMessage returns and the response is sent
	// to the Agent the OnConnectionClose message will be called immediately.
	OnMessage func(ctx context.Context, conn Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent

	// OnConnectionClose is called when the OpAMP connection is closed.
	OnConnectionClose func(conn Connection)
}

func (c *ConnectionCallbacks) SetDefaults() {
	if c.OnConnected == nil {
		c.OnConnected = func(ctx context.Context, conn Connection) {}
	}

	if c.OnMessage == nil {
		c.OnMessage = func(
			ctx context.Context, conn Connection, message *protobufs.AgentToServer,
		) *protobufs.ServerToAgent {
			// We will send an empty response since there is no user-defined callback to handle it.
			return &protobufs.ServerToAgent{
				InstanceUid: message.InstanceUid,
			}
		}
	}

	if c.OnConnectionClose == nil {
		c.OnConnectionClose = func(conn Connection) {}
	}
}
