package server

import (
	"context"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

type connection struct {
	wsConn *websocket.Conn
	header http.Header
}

var _ types.Connection = (*connection)(nil)

func (c connection) RemoteAddr() net.Addr {
	return c.RemoteAddr()
}

func (c connection) Header() http.Header {
	return c.header
}

func (c connection) Send(ctx context.Context, message *protobufs.ServerToAgent) error {
	bytes, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	return c.wsConn.WriteMessage(websocket.BinaryMessage, bytes)
}
