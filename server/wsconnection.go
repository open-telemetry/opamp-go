package server

import (
	"context"
	"net"

	"github.com/gorilla/websocket"

	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// wsConnection represents a persistent OpAMP connection over a WebSocket.
type wsConnection struct {
	wsConn *websocket.Conn
}

var _ types.Connection = (*wsConnection)(nil)

func (c wsConnection) RemoteAddr() net.Addr {
	return c.wsConn.RemoteAddr()
}

// Message header is currently uint64 zero value.
const wsMsgHeader = uint64(0)

func (c wsConnection) Send(_ context.Context, message *protobufs.ServerToAgent) error {
	return internal.WriteWSMessage(c.wsConn, message)
}

func (c wsConnection) Disconnect() error {
	return c.wsConn.Close()
}
