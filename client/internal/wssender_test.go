package internal

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestWSSenderWriteWSMessageFailure(t *testing.T) {
	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	// After accepting the WebSocket, close the underlying TCP connection
	// after a short delay so the client's send will hit the closed connection
	// and fail with connection reset.
	srv.OnWSConnect = func(conn *websocket.Conn) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
				// setting linger to 0 to prevent os from doing graceful close
				_ = tcpConn.SetLinger(0)
			}
			_ = conn.UnderlyingConn().Close()
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
	time.Sleep(500 * time.Millisecond)
	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.InstanceUid = []byte("test-instance-uid-16b")
	})
	sender.ScheduleSend()

	// run() should see the connection-reset error from sendMessage and close s.stopped.
	select {
	case <-sender.IsStopped():
		// Expected: sender stopped and closed s.stopped
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not close s.stopped within 5s after WriteWSMessage failure")
	}

	// StoppingErr() should report the connection reset error.
	stoppingErr := sender.StoppingErr()
	require.Error(t, stoppingErr)
	assert.True(t, isConnectionResetError(stoppingErr),
		"StoppingErr() should be a connection reset error, got: %v", stoppingErr)
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
