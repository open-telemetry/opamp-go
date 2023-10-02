package client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestDisconnectWSByServer(t *testing.T) {
	// Start a Server.
	srv := internal.StartMockServer(t)

	var conn atomic.Value
	srv.OnWSConnect = func(c *websocket.Conn) {
		conn.Store(c)
	}

	// Start an OpAMP/WebSocket client.
	var connected int64
	var connectErr atomic.Value
	settings := types.StartSettings{
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.StoreInt64(&connected, 1)
			},
			OnConnectFailedFunc: func(err error) {
				connectErr.Store(err)
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := NewWebSocket(nil)
	startClient(t, settings, client)

	// Wait for connection to be established.
	eventually(t, func() bool { return conn.Load() != nil })
	assert.True(t, connectErr.Load() == nil)

	// Close the Server and forcefully disconnect.
	srv.Close()
	_ = conn.Load().(*websocket.Conn).Close()

	// The client must retry and must fail now.
	eventually(t, func() bool { return connectErr.Load() != nil })

	// Stop the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestVerifyWSCompress(t *testing.T) {

	tests := []bool{false, true}
	for _, withCompression := range tests {
		t.Run(fmt.Sprintf("%v", withCompression), func(t *testing.T) {

			// Start a Server.
			srv := internal.StartMockServer(t)
			srv.EnableExpectMode()
			if withCompression {
				srv.EnableCompression()
			}

			// We use a transparent TCP proxy to be able to count the actual bytes transferred so that
			// we can test the number of actual bytes vs number of expected bytes with and without compression.
			proxy := testhelpers.NewProxy(srv.Endpoint)
			assert.NoError(t, proxy.Start())

			// Start an OpAMP/WebSocket client.
			var clientGotRemoteConfig atomic.Value
			settings := types.StartSettings{
				Callbacks: types.CallbacksStruct{
					OnMessageFunc: func(ctx context.Context, msg *types.MessageData) {
						if msg.RemoteConfig != nil {
							clientGotRemoteConfig.Store(msg.RemoteConfig)
						}
					},
					GetEffectiveConfigFunc: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
						// If the client already received a remote config offer make sure to report
						// the effective config back to the server.
						var effCfg []byte
						remoteCfg, _ := clientGotRemoteConfig.Load().(*protobufs.AgentRemoteConfig)
						if remoteCfg != nil {
							effCfg = remoteCfg.Config.ConfigMap[""].Body
						}
						return &protobufs.EffectiveConfig{
							ConfigMap: &protobufs.AgentConfigMap{
								ConfigMap: map[string]*protobufs.AgentConfigFile{
									"key": {
										Body: effCfg,
									},
								},
							},
						}, nil
					},
				},
				Capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
					protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig,
			}
			settings.OpAMPServerURL = "ws://" + proxy.IncomingEndpoint()

			if withCompression {
				settings.EnableCompression = true
			}

			client := NewWebSocket(nil)
			startClient(t, settings, client)

			// Use highly compressible config body.
			uncompressedCfg := []byte(strings.Repeat("test", 10000))

			remoteCfg := &protobufs.AgentRemoteConfig{
				Config: &protobufs.AgentConfigMap{
					ConfigMap: map[string]*protobufs.AgentConfigFile{
						"": &protobufs.AgentConfigFile{
							Body: uncompressedCfg,
						},
					},
				},
				ConfigHash: []byte{1, 2, 3, 4},
			}

			// ---> Server
			srv.Expect(
				func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
					assert.EqualValues(t, 0, msg.SequenceNum)
					// The first status report after Start must have full AgentDescription.
					assert.True(t, proto.Equal(client.AgentDescription(), msg.AgentDescription))
					return &protobufs.ServerToAgent{
						InstanceUid:  msg.InstanceUid,
						RemoteConfig: remoteCfg,
					}
				},
			)

			// Wait to receive remote config
			eventually(t, func() bool { return clientGotRemoteConfig.Load() != nil })

			_ = client.UpdateEffectiveConfig(context.Background())

			// ---> Server
			srv.Expect(
				func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
					return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
				},
			)

			// Stop the client.
			err := client.Stop(context.Background())
			assert.NoError(t, err)

			proxy.Stop()

			fmt.Printf("sent %d, received %d\n", proxy.ClientToServerBytes(), proxy.ServerToClientBytes())

			if withCompression {
				// With compression the entire bytes exchanged should be less than the config body.
				// This is only possible if there is any compression happening.
				assert.Less(t, proxy.ServerToClientBytes(), len(uncompressedCfg))
				assert.Less(t, proxy.ClientToServerBytes(), len(uncompressedCfg))
			} else {
				// Without compression the entire bytes exchanged should be more than the config body.
				assert.Greater(t, proxy.ServerToClientBytes(), len(uncompressedCfg))
				assert.Greater(t, proxy.ClientToServerBytes(), len(uncompressedCfg))
			}
		})
	}
}

func TestHandlesStopBeforeStart(t *testing.T) {
	client := NewWebSocket(nil)
	require.Error(t, client.Stop(context.Background()))
}

func TestPerformsClosingHandshake(t *testing.T) {
	srv := internal.StartMockServer(t)
	var wsConn *websocket.Conn
	connected := make(chan struct{})
	closed := make(chan struct{})

	srv.OnWSConnect = func(conn *websocket.Conn) {
		wsConn = conn
		connected <- struct{}{}
	}

	client := NewWebSocket(nil)
	startClient(t, types.StartSettings{
		OpAMPServerURL: srv.GetHTTPTestServer().URL,
	}, client)

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		require.Fail(t, "Connection never established")
	}

	require.Eventually(t, func() bool {
		client.connMutex.RLock()
		conn := client.conn
		client.connMutex.RUnlock()
		return conn != nil
	}, 2*time.Second, 250*time.Millisecond)

	defHandler := wsConn.CloseHandler()

	wsConn.SetCloseHandler(func(code int, _ string) error {
		require.Equal(t, websocket.CloseNormalClosure, code, "Client sent non-normal closing code")

		err := defHandler(code, "")
		closed <- struct{}{}
		return err
	})

	client.Stop(context.Background())

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		require.Fail(t, "Connection never closed")
	}
}

func TestHandlesSlowCloseMessageFromServer(t *testing.T) {
	srv := internal.StartMockServer(t)
	var wsConn *websocket.Conn
	connected := make(chan struct{})
	closed := make(chan struct{})

	srv.OnWSConnect = func(conn *websocket.Conn) {
		wsConn = conn
		connected <- struct{}{}
	}

	client := NewWebSocket(nil)
	client.connShutdownTimeout = 100 * time.Millisecond
	startClient(t, types.StartSettings{
		OpAMPServerURL: srv.GetHTTPTestServer().URL,
	}, client)

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		require.Fail(t, "Connection never established")
	}

	require.Eventually(t, func() bool {
		client.connMutex.RLock()
		conn := client.conn
		client.connMutex.RUnlock()
		return conn != nil
	}, 2*time.Second, 250*time.Millisecond)

	defHandler := wsConn.CloseHandler()

	wsConn.SetCloseHandler(func(code int, _ string) error {
		require.Equal(t, websocket.CloseNormalClosure, code, "Client sent non-normal closing code")

		time.Sleep(200 * time.Millisecond)
		err := defHandler(code, "")
		closed <- struct{}{}
		return err
	})

	client.Stop(context.Background())

	select {
	case <-closed:
	case <-time.After(1 * time.Second):
		require.Fail(t, "Connection never closed")
	}
}

func TestHandlesNoCloseMessageFromServer(t *testing.T) {
	srv := internal.StartMockServer(t)
	var wsConn *websocket.Conn
	connected := make(chan struct{})
	closed := make(chan struct{})

	srv.OnWSConnect = func(conn *websocket.Conn) {
		wsConn = conn
		connected <- struct{}{}
	}

	client := NewWebSocket(nil)
	client.connShutdownTimeout = 100 * time.Millisecond
	startClient(t, types.StartSettings{
		OpAMPServerURL: srv.GetHTTPTestServer().URL,
	}, client)

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		require.Fail(t, "Connection never established")
	}

	require.Eventually(t, func() bool {
		client.connMutex.RLock()
		conn := client.conn
		client.connMutex.RUnlock()
		return conn != nil
	}, 2*time.Second, 250*time.Millisecond)

	wsConn.SetCloseHandler(func(code int, _ string) error {
		// Don't send close message
		return nil
	})

	go func() {
		client.Stop(context.Background())
		closed <- struct{}{}
	}()

	select {
	case <-closed:
	case <-time.After(1 * time.Second):
		require.Fail(t, "Connection never closed")
	}
}

func TestHandlesConnectionError(t *testing.T) {
	srv := internal.StartMockServer(t)
	var wsConn *websocket.Conn
	connected := make(chan struct{})

	srv.OnWSConnect = func(conn *websocket.Conn) {
		wsConn = conn
		connected <- struct{}{}
	}

	client := NewWebSocket(nil)
	startClient(t, types.StartSettings{
		OpAMPServerURL: srv.GetHTTPTestServer().URL,
	}, client)

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		require.Fail(t, "Connection never established")
	}

	require.Eventually(t, func() bool {
		client.connMutex.RLock()
		conn := client.conn
		client.connMutex.RUnlock()
		return conn != nil
	}, 2*time.Second, 250*time.Millisecond)

	// Write an invalid message to the connection. The client
	// will take this as an error and reconnect to the server.
	writer, err := wsConn.NextWriter(websocket.BinaryMessage)
	require.NoError(t, err)
	n, err := writer.Write([]byte{99, 1, 2, 3, 4, 5})
	require.NoError(t, err)
	require.Equal(t, 6, n)
	err = writer.Close()
	require.NoError(t, err)

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		require.Fail(t, "Connection never re-established")
	}

	require.Eventually(t, func() bool {
		client.connMutex.RLock()
		conn := client.conn
		client.connMutex.RUnlock()
		return conn != nil
	}, 2*time.Second, 250*time.Millisecond)

	err = client.Stop(context.Background())
	require.NoError(t, err)
}

func TestDisallowsSendingAfterStopped(t *testing.T) {
	srv := internal.StartMockServer(t)
	var wsConn *websocket.Conn
	connected := make(chan struct{})
	closed := make(chan struct{})

	srv.OnWSConnect = func(conn *websocket.Conn) {
		wsConn = conn
		connected <- struct{}{}
	}

	client := NewWebSocket(nil)
	startClient(t, types.StartSettings{
		OpAMPServerURL: srv.GetHTTPTestServer().URL,
	}, client)

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		require.Fail(t, "Connection never established")
	}

	require.Eventually(t, func() bool {
		client.connMutex.RLock()
		conn := client.conn
		client.connMutex.RUnlock()
		return conn != nil
	}, 2*time.Second, 250*time.Millisecond)

	wg := sync.WaitGroup{}
	send := make(chan struct{})

	defHandler := wsConn.CloseHandler()
	wsConn.SetCloseHandler(func(code int, _ string) error {
		close(send)
		// Pause the stopping process to ensure that sends are disallowed while the client
		// is stopping, not necessarily just after it has stopped.
		wg.Wait()
		err := defHandler(code, "")
		closed <- struct{}{}
		return err
	})

	wg.Add(5)
	go func() {
		err := client.Stop(context.Background())
		require.NoError(t, err)
	}()
	go func() {
		<-send
		err := client.SetAgentDescription(&protobufs.AgentDescription{})
		require.Error(t, err)
		wg.Done()
	}()
	go func() {
		<-send
		err := client.SetHealth(&protobufs.AgentHealth{})
		require.Error(t, err)
		wg.Done()
	}()
	go func() {
		<-send
		err := client.UpdateEffectiveConfig(context.Background())
		require.Error(t, err)
		wg.Done()
	}()
	go func() {
		<-send
		err := client.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{})
		require.Error(t, err)
		wg.Done()
	}()
	go func() {
		<-send
		err := client.SetPackageStatuses(&protobufs.PackageStatuses{})
		require.Error(t, err)
		wg.Done()
	}()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Error("Connection failed to close")
	}
}
