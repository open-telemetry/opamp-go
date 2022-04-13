package server

import (
	"context"
	"errors"
	"net"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

var errInvalidHTTPConnection = errors.New("cannot Send() over HTTP connection")

// httpConnection represents an OpAMP connection over a plain HTTP connection.
// Only one response is possible to send when using plain HTTP connection
// and that response will be sent by OpAMP server's HTTP request handler after the
// onMessage callback returns.
type httpConnection struct {
	conn net.Conn
}

func (c httpConnection) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

var _ types.Connection = (*httpConnection)(nil)

func (c httpConnection) Send(_ context.Context, _ *protobufs.ServerToAgent) error {
	// Send() should not be called for plain HTTP connection. Instead, the response will
	// be sent after the onMessage callback returns.
	return errInvalidHTTPConnection
}
