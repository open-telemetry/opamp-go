package types

import (
	"net/http"

	"github.com/open-telemetry/opamp-go/protobufs"
)

// ConnectionResponse is the return type of the OnConnecting callback.
type ConnectionResponse struct {
	Accept             bool
	HTTPStatusCode     int
	HTTPResponseHeader map[string]string
}

type Callbacks interface {
	// The following callbacks will never be called concurrently for the same
	// connection. They may be called concurrently for different connections.

	// OnConnecting is called when there is a new incoming connection.
	// The handler can examine the request and either accept or reject the connection.
	// To accept:
	//   Return ConnectionResponse with Accept=true.
	//   HTTPStatusCode and HTTPResponseHeader are ignored.
	//
	// To reject:
	//   Return ConnectionResponse with Accept=false. HTTPStatusCode MUST be set to
	//   non-zero value to indicate the rejection reason (typically 401, 429 or 503).
	//   HTTPResponseHeader may be optionally set (e.g. "Retry-After: 30").
	OnConnecting(request *http.Request) ConnectionResponse

	// OnConnected is called when the WebSocket connection is successfully established
	// after OnConnecting() returns and the HTTP connection is upgraded to WebSocket.
	OnConnected(conn Connection)

	// OnMessage is called when a message is received from the connection. Can happen
	// only after OnConnected().
	OnMessage(conn Connection, message *protobufs.AgentToServer)

	// OnConnectionClose is called when the WebSocket connection is closed.
	// Typically, preceded by OnDisconnect() unless the client misbehaves or the
	// connection is lost.
	OnConnectionClose(conn Connection)
}
