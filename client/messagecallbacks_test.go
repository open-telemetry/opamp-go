package client

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestMessageSentAndReceivedCallbacks(t *testing.T) {
	testClients(t, func(t *testing.T, client OpAMPClient) {
		srv := internal.StartMockServer(t)
		srv.SetOnMessage(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
		})

		var sentCount, receivedCount atomic.Int64
		var sentSizeOK, receivedSizeOK atomic.Bool
		sentSizeOK.Store(true)
		receivedSizeOK.Store(true)

		settings := types.StartSettings{
			Callbacks: types.Callbacks{
				OnMessageSent: func(ctx context.Context, msg *protobufs.AgentToServer, size int) {
					// Callback size is the protobuf payload, independent of transport framing.
					if msg == nil || size != proto.Size(msg) {
						sentSizeOK.Store(false)
					}
					sentCount.Add(1)
				},
				OnMessageReceived: func(ctx context.Context, msg *protobufs.ServerToAgent, size int) {
					if msg == nil || size != proto.Size(msg) {
						receivedSizeOK.Store(false)
					}
					receivedCount.Add(1)
				},
			},
		}
		settings.OpAMPServerURL = "ws://" + srv.Endpoint
		startClient(t, settings, client)

		eventually(t, func() bool { return sentCount.Load() > 0 && receivedCount.Load() > 0 })
		assert.True(t, sentSizeOK.Load(), "OnMessageSent reported an invalid size")
		assert.True(t, receivedSizeOK.Load(), "OnMessageReceived reported an invalid size")

		err := client.Stop(context.Background())
		assert.NoError(t, err)
		srv.Close()
	})
}

func TestOnDisconnectCalledWhenServerDropsConnection(t *testing.T) {
	srv := internal.StartMockServer(t)

	var conn atomic.Value
	srv.SetOnWSConnect(func(c *websocket.Conn) {
		conn.Store(c)
	})

	var connected, disconnected atomic.Int64
	var disconnectErr atomic.Value
	settings := types.StartSettings{
		Callbacks: types.Callbacks{
			OnConnect: func(ctx context.Context) {
				connected.Add(1)
			},
			OnDisconnect: func(ctx context.Context, err error) {
				if err != nil {
					disconnectErr.Store(err)
				}
				disconnected.Add(1)
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := NewWebSocket(nil)
	startClient(t, settings, client)

	eventually(t, func() bool { return conn.Load() != nil && connected.Load() == 1 })
	assert.EqualValues(t, 0, disconnected.Load())

	// Drop the connection from the Server side.
	srv.Close()
	_ = conn.Load().(*websocket.Conn).Close()

	eventually(t, func() bool { return disconnected.Load() == 1 })
	assert.NotNil(t, disconnectErr.Load(), "OnDisconnect should report the reason")

	err := client.Stop(context.Background())
	assert.NoError(t, err)
	assert.EqualValues(t, 1, disconnected.Load())
}

func TestOnDisconnectCalledWhenInitialWebSocketMessageFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	serverReady := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		acceptHash := sha1.Sum([]byte(request.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(acceptHash[:]) + "\r\n\r\n"))
		close(serverReady)
	}()

	var connected, disconnected atomic.Int64
	settings := types.StartSettings{
		Callbacks: types.Callbacks{
			OnConnect: func(context.Context) {
				connected.Add(1)
			},
			OnDisconnect: func(_ context.Context, err error) {
				assert.Error(t, err)
				disconnected.Add(1)
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + listener.Addr().String()
	client := NewWebSocket(nil)
	startClient(t, settings, client)

	select {
	case <-serverReady:
	case <-time.After(time.Second):
		t.Fatal("server did not complete the WebSocket handshake")
	}
	eventually(t, func() bool { return connected.Load() > 0 })
	eventually(t, func() bool { return disconnected.Load() > 0 })
	assert.Equal(t, connected.Load(), disconnected.Load())

	assert.NoError(t, client.Stop(context.Background()))
}

func TestOnDisconnectNotCalledOnStop(t *testing.T) {
	srv := internal.StartMockServer(t)

	var connected, disconnected atomic.Int64
	settings := types.StartSettings{
		Callbacks: types.Callbacks{
			OnConnect: func(ctx context.Context) {
				connected.Add(1)
			},
			OnDisconnect: func(ctx context.Context, err error) {
				disconnected.Add(1)
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := NewWebSocket(nil)
	startClient(t, settings, client)

	eventually(t, func() bool { return connected.Load() == 1 })

	err := client.Stop(context.Background())
	assert.NoError(t, err)
	assert.EqualValues(t, 0, disconnected.Load())

	srv.Close()
}
