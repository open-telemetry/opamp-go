//go:build cwebsocket

package internal

import (
	"context"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// dialCoder dials a coder/websocket connection, removes the default 32 KB read
// limit (OpAMP does not impose one), and registers CloseNow for test cleanup.
func dialCoder(ctx context.Context, t *testing.T, addr string, opts *websocket.DialOptions) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, addr, opts)
	require.NoError(t, err)
	conn.SetReadLimit(-1)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}
