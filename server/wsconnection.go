package server

import (
	"context"
	"net"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

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

func (c wsConnection) Send(_ context.Context, message *protobufs.ServerToAgent) error {
	bytes, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	return c.wsConn.WriteMessage(websocket.BinaryMessage, bytes)
}

func (c wsConnection) Disconnect() error {
	return c.wsConn.Close()
}
