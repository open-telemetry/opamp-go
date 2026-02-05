package internal

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestWSSenderWriteWSMessageFailure_BrokenPipe(t *testing.T) {
	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	// After accepting the WebSocket, close the underlying TCP connection
	// after a short delay so the client's send will hit the closed connection
	// and fail with connection reset.
	connCloseCh := make(chan error)
	srv.OnWSConnect = func(conn *websocket.Conn) {
		go func() {
			if tcpConn, ok := conn.NetConn().(*net.TCPConn); ok {
				// setting linger to 0 to prevent os from doing graceful close
				_ = tcpConn.SetLinger(0)
			}
			// close may or may not block until any buffered data is sent, but in our case
			// it wont block since we are setting linger to 0 so the moment close returns
			// we can be sure that connection is killed
			connCloseCh <- conn.NetConn().Close()
		}()
	}

	conn, _, err := websocket.DefaultDialer.DialContext(
		context.Background(),
		"ws://"+srv.Endpoint,
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	sender := NewSender(&sharedinternal.NopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// invoke Start to trigger the run goroutine
	err = sender.Start(ctx, conn)
	require.NoError(t, err)

	// wait for server to close underlying conn, then schedule send
	select {
	case err := <-connCloseCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("failed to close the tcp connection to simulate failure")
	}
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

	// StoppingErr() should report either ECONNRESET or EPIPE
	stoppingErr := sender.StoppingErr()
	require.Error(t, stoppingErr)
	assert.True(t, isConnectionResetError(stoppingErr),
		"StoppingErr() should be a connection reset error, got: %v", stoppingErr)
}

func TestWSSenderWriteWSMessageFailure_ConnectionTimeout(t *testing.T) {
	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.DialContext(
		context.Background(),
		"ws://"+srv.Endpoint,
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	sender := NewSender(&sharedinternal.NopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err = sender.Start(ctx, conn)
	require.NoError(t, err)

	// setting a already passed deadline
	_ = conn.SetWriteDeadline(time.Now().Add(-1 * time.Second))

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

func TestWSSenderSetHeartbeatInterval(t *testing.T) {
	sender := NewSender(&sharedinternal.NopLogger{})

	// Default interval should be 30s as per OpAMP Specification
	assert.Equal(t, int64((30 * time.Second).Milliseconds()), sender.heartbeatIntervalMs.Load())

	// negative interval is invalid for http sender
	assert.Error(t, sender.SetHeartbeatInterval(-1))
	assert.Equal(t, int64((30 * time.Second).Milliseconds()), sender.heartbeatIntervalMs.Load())

	// zero is valid for ws sender
	assert.NoError(t, sender.SetHeartbeatInterval(0))
	assert.Equal(t, int64(0), sender.heartbeatIntervalMs.Load())

	var expected int64 = 10000
	assert.NoError(t, sender.SetHeartbeatInterval(time.Duration(expected)*time.Millisecond))
	assert.Equal(t, expected, sender.heartbeatIntervalMs.Load())
}
