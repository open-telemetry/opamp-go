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

type Callbacks interface {
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
	OnConnecting(request *http.Request) ConnectionResponse
}

// ConnectionCallbacks receives callbacks for a specific connection. An implementation of
// this interface MUST be set on the ConnectionResponse returned by the OnConnecting
// callback if Accept=true. The implementation can be shared by all connections or can be
// unique for each connection.
type ConnectionCallbacks interface {
	// The following callbacks will never be called concurrently for the same
	// connection. They may be called concurrently for different connections.

	// OnConnected is called when an incoming OpAMP connection is successfully
	// established after OnConnecting() returns.
	OnConnected(ctx context.Context, conn Connection)

	// OnMessage is called when a message is received from the connection. Can happen
	// only after OnConnected(). Must return a ServerToAgent message that will be sent
	// as a response to the Agent.
	// For plain HTTP requests once OnMessage returns and the response is sent
	// to the Agent the OnConnectionClose message will be called immediately.
	OnMessage(ctx context.Context, conn Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent

	// OnConnectionClose is called when the OpAMP connection is closed.
	OnConnectionClose(conn Connection)
}
