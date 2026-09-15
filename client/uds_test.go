//go:build !windows

package client

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	servertypes "github.com/open-telemetry/opamp-go/server/types"
)

// unixSocketServer is a test OpAMP server listening on a Unix domain socket.
type unixSocketServer struct {
	socketPath string
	gotMessage chan *protobufs.AgentToServer
}

// startUnixSocketServer starts an OpAMP server on a filesystem-path Unix domain
// socket using the server-side Listener injection. No TCP/UDP port is opened.
func startUnixSocketServer(t *testing.T) *unixSocketServer {
	// t.TempDir() paths can exceed the 104-byte sun_path limit on macOS, so use a
	// short temp dir instead.
	dir, err := os.MkdirTemp("", "o")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	s := &unixSocketServer{
		socketPath: filepath.Join(dir, "opamp.sock"),
		gotMessage: make(chan *protobufs.AgentToServer, 1),
	}

	ln, err := net.Listen("unix", s.socketPath)
	require.NoError(t, err)

	callbacks := servertypes.Callbacks{
		OnConnecting: func(_ *http.Request) servertypes.ConnectionResponse {
			return servertypes.ConnectionResponse{
				Accept: true,
				ConnectionCallbacks: servertypes.ConnectionCallbacks{
					OnMessage: func(_ context.Context, conn servertypes.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
						// The underlying conn must be a Unix socket so that the
						// consumer can authenticate the peer via SO_PEERCRED.
						_, ok := conn.Connection().(*net.UnixConn)
						assert.True(t, ok, "server-side connection should be a *net.UnixConn")
						select {
						case s.gotMessage <- message:
						default:
						}
						return &protobufs.ServerToAgent{InstanceUid: message.InstanceUid}
					},
				},
			}
		},
	}

	srv := server.New(nil)
	require.NoError(t, srv.Start(server.StartSettings{
		Settings:   server.Settings{Callbacks: callbacks},
		Listener:   ln,
		ListenPath: "/v1/opamp",
	}))
	t.Cleanup(func() { srv.Stop(context.Background()) })

	// The server must be listening on the Unix socket, not on a TCP/UDP port.
	require.Equal(t, "unix", srv.Addr().Network())
	require.Equal(t, s.socketPath, srv.Addr().String())
	return s
}

func (s *unixSocketServer) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", s.socketPath)
}

// requireMessage waits for the first AgentToServer message, which proves the
// full handshake over the Unix socket completed.
func (s *unixSocketServer) requireMessage(t *testing.T) {
	select {
	case msg := <-s.gotMessage:
		assert.NotNil(t, msg)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first AgentToServer message over the Unix socket")
	}
}

// TestWSClientOverUnixSocket exercises the full WebSocket OpAMP handshake over a
// Unix domain socket using the client-side DialContext hook.
func TestWSClientOverUnixSocket(t *testing.T) {
	srv := startUnixSocketServer(t)

	client := NewWebSocket(nil)
	settings := types.StartSettings{
		// The URL host is cosmetic; DialContext dials the socket regardless.
		OpAMPServerURL: "ws://localhost/v1/opamp",
		DialContext:    srv.dialContext,
	}
	prepareClient(t, &settings, client)
	require.NoError(t, client.Start(context.Background(), settings))
	defer client.Stop(context.Background())

	srv.requireMessage(t)
}

// TestHTTPClientOverUnixSocket exercises the plain HTTP OpAMP transport over a
// Unix domain socket using the client-side DialContext hook.
func TestHTTPClientOverUnixSocket(t *testing.T) {
	srv := startUnixSocketServer(t)

	client := NewHTTP(nil)
	settings := types.StartSettings{
		OpAMPServerURL: "http://localhost/v1/opamp",
		DialContext:    srv.dialContext,
	}
	prepareClient(t, &settings, client)
	require.NoError(t, client.Start(context.Background(), settings))
	defer client.Stop(context.Background())

	srv.requireMessage(t)
}

// TestHTTPClientDialContextRejectsCustomTransport verifies the HTTP transport
// fails loudly when DialContext cannot be applied to a non-*http.Transport
// Client, instead of silently dropping it.
func TestHTTPClientDialContextRejectsCustomTransport(t *testing.T) {
	client := NewHTTP(nil)
	settings := types.StartSettings{
		OpAMPServerURL: "http://localhost/v1/opamp",
		Client:         &http.Client{Transport: &recordingRoundTripper{base: http.DefaultTransport}},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return nil, nil
		},
	}
	prepareClient(t, &settings, client)
	require.Error(t, client.Start(context.Background(), settings))
}

// TestDialContextRejectsProxy verifies Start fails when both DialContext and
// ProxyURL are set, since DialContext replaces the dialing proxying relies on.
func TestDialContextRejectsProxy(t *testing.T) {
	testClients(t, func(t *testing.T, client OpAMPClient) {
		settings := types.StartSettings{
			OpAMPServerURL: "ws://localhost/v1/opamp",
			ProxyURL:       "http://localhost:3128",
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return nil, nil
			},
		}
		prepareClient(t, &settings, client)
		require.ErrorIs(t, client.Start(context.Background(), settings), internal.ErrDialContextAndProxyURL)
	})
}
