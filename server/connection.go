package server

import (
	"context"
	"net"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

type connection struct {
	wsConn *websocket.Conn
}

var _ types.Connection = (*connection)(nil)

func (c connection) RemoteAddr() net.Addr {
	return c.wsConn.RemoteAddr()
}

func (c connection) Send(ctx context.Context, message *protobufs.ServerToAgent) error {
	bytes, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	return c.wsConn.WriteMessage(websocket.BinaryMessage, bytes)
}
