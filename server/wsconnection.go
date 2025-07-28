package server

import (
	"context"
	"net"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// wsConnection represents a persistent OpAMP connection over a WebSocket.
type wsConnection struct {
	// The websocket library does not allow multiple concurrent write operations,
	// so ensure that we only have a single operation in progress at a time.
	// For more: https://pkg.go.dev/github.com/gorilla/websocket#hdr-Concurrency
	connMutex         *sync.Mutex
	wsConn            *websocket.Conn
	sendErrorCallback func(message *protobufs.ServerToAgent, err error)
}

var _ types.Connection = (*wsConnection)(nil)

func (c wsConnection) Connection() net.Conn {
	return c.wsConn.UnderlyingConn()
}

func (c wsConnection) Send(_ context.Context, message *protobufs.ServerToAgent) error {
	c.connMutex.Lock()
	defer c.connMutex.Unlock()

	err := internal.WriteWSMessage(c.wsConn, message)
	if err != nil {
		c.sendErrorCallback(message, err)
	}
	return err
}

func (c wsConnection) Disconnect() error {
	return c.wsConn.Close()
}
