package client

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
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
					// The reported size covers at least the encoded Protobuf payload.
					if msg == nil || size <= 0 || size < proto.Size(msg) {
						sentSizeOK.Store(false)
					}
					sentCount.Add(1)
				},
				OnMessageReceived: func(ctx context.Context, msg *protobufs.ServerToAgent, size int) {
					if msg == nil || size <= 0 || size < proto.Size(msg) {
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
