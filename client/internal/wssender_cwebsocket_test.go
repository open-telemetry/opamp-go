//go:build cwebsocket

package internal

import (
	"context"
	"errors"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestWSSenderWriteWSMessageFailure_BrokenPipe(t *testing.T) {
	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	// Simulate a broken pipe by injecting a connection-reset error on the
	// client-side transport. websocket.NetConn is not suitable here because its
	// Close performs a graceful WebSocket close handshake (blocks up to 10 s).
	var connShim *connResetErrConn
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			connShim = &connResetErrConn{Conn: c}
			return connShim, nil
		},
	}

	conn, _, err := websocket.Dial(
		context.Background(),
		"ws://"+srv.Endpoint,
		&websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}},
	)
	require.NoError(t, err)
	conn.SetReadLimit(-1)
	t.Cleanup(func() { _ = conn.CloseNow() })

	sender := NewSender(&sharedinternal.NopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err = sender.Start(ctx, conn)
	require.NoError(t, err)

	// Trigger write failure injection, then schedule a send.
	connShim.failWrite.Store(true)
	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.InstanceUid = []byte("test-instance-uid-16b")
	})
	sender.ScheduleSend()

	// run() should see the connection-reset error from sendMessage and close s.stopped.
	select {
	case <-sender.IsStopped():
		// Expected: sender stopped and closed s.stopped
	case <-time.After(3 * time.Second):
		t.Fatal("sender did not close s.stopped within 3s after WriteWSMessage failure")
	}

	// StoppingErr() should report ECONNRESET.
	stoppingErr := sender.StoppingErr()
	require.Error(t, stoppingErr)
	assert.True(t, isConnectionResetError(stoppingErr),
		"StoppingErr() should be a connection reset error, got: %v", stoppingErr)
}

type connResetErrConn struct {
	net.Conn
	failWrite atomic.Bool
}

func (c *connResetErrConn) Write(b []byte) (n int, err error) {
	if c.failWrite.Load() {
		switch runtime.GOOS {
		case "windows":
			// wsaECONNABORTED (10053) is already caught by isConnectionResetError
			// on Windows; WSAECONNRESET (10054) is not in the cross-platform syscall pkg.
			return 0, &net.OpError{Op: "write", Err: syscall.Errno(wsaECONNABORTED)}
		default:
			return 0, &net.OpError{Op: "write", Err: syscall.Errno(syscall.ECONNRESET)}
		}
	}
	return c.Conn.Write(b)
}

func TestWSSenderWriteWSMessageFailure_ConnectionTimeout(t *testing.T) {
	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	// Capture the underlying TCP connection via a custom transport so we can
	// set a write deadline on it (coder's *Conn does not expose SetWriteDeadline).
	var rawConn net.Conn
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			rawConn = c
			return c, nil
		},
	}
	conn, _, err := websocket.Dial(
		context.Background(),
		"ws://"+srv.Endpoint,
		&websocket.DialOptions{
			HTTPClient: &http.Client{Transport: transport},
		},
	)
	require.NoError(t, err)
	conn.SetReadLimit(-1)
	t.Cleanup(func() { _ = conn.CloseNow() })

	sender := NewSender(&sharedinternal.NopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err = sender.Start(ctx, conn)
	require.NoError(t, err)

	// Set a write deadline in the past on the raw TCP connection.
	_ = rawConn.SetWriteDeadline(time.Now().Add(-1 * time.Second))

	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.InstanceUid = make([]byte, 1024*1024)
	})
	sender.ScheduleSend()

	select {
	case <-sender.IsStopped():
		t.Log("Sender stopped successfully")
	case <-time.After(3 * time.Second):
		t.Fatal("sender did not stop within 3s")
	}

	stoppingErr := sender.StoppingErr()
	t.Logf("Stopping error: %v", stoppingErr)
	require.Error(t, stoppingErr)

	var netErr net.Error
	require.True(t, errors.As(stoppingErr, &netErr))
	require.Equal(t, true, netErr.Timeout())
}

func TestWSSenderWriteWSMessageFailure_ConnAborted(t *testing.T) {
	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	// Custom transport to inject our wrapped connection to simulate this case, since this case is usually hit when
	// some local software in the system messes with the connection
	var connShim *connAbortedErrConn
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			connShim = &connAbortedErrConn{Conn: c}
			return connShim, nil
		},
	}

	conn, _, err := websocket.Dial(
		context.Background(),
		"ws://"+srv.Endpoint,
		&websocket.DialOptions{
			HTTPClient: &http.Client{Transport: transport},
		},
	)
	require.NoError(t, err)
	conn.SetReadLimit(-1)
	t.Cleanup(func() { _ = conn.CloseNow() })

	sender := NewSender(&sharedinternal.NopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err = sender.Start(ctx, conn)
	require.NoError(t, err)

	// Enable error injection
	connShim.failWrite.Store(true)

	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.InstanceUid = []byte("test-instance-uid-abort")
	})
	sender.ScheduleSend()

	select {
	case <-sender.IsStopped():
		// Expected: sender stopped
	case <-time.After(3 * time.Second):
		t.Fatal("sender did not stop within 3s")
	}

	stoppingErr := sender.StoppingErr()
	require.Error(t, stoppingErr)
	var opErr *net.OpError
	require.True(t, errors.As(stoppingErr, &opErr))
	require.True(t, opErr.Err == syscall.ECONNABORTED || opErr.Err == syscall.Errno(wsaECONNABORTED))
}

type connAbortedErrConn struct {
	net.Conn
	failWrite atomic.Bool
}

func (c *connAbortedErrConn) Write(b []byte) (n int, err error) {
	if c.failWrite.Load() {
		switch runtime.GOOS {
		case "windows":
			return 0, &net.OpError{
				Op:  "write",
				Err: syscall.Errno(wsaECONNABORTED),
			}
		default:
			return 0, &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.ECONNABORTED),
			}
		}
	}
	return c.Conn.Write(b)
}
