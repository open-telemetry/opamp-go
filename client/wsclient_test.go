package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
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
			OnConnectFunc: func(ctx context.Context) {
				atomic.StoreInt64(&connected, 1)
			},
			OnConnectFailedFunc: func(ctx context.Context, err error) {
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

func redirectServer(to string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, to, http.StatusSeeOther)
	}))
}

func errServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(302)
	}))
}

func TestRedirectWS(t *testing.T) {
	redirectee := internal.StartMockServer(t)
	tests := []struct {
		Name       string
		Redirector *httptest.Server
		ExpError   bool
	}{
		{
			Name:       "redirect ws scheme",
			Redirector: redirectServer("ws://"+redirectee.Endpoint, 302),
		},
		{
			Name:       "redirect http scheme",
			Redirector: redirectServer("http://"+redirectee.Endpoint, 302),
		},
		{
			Name:       "missing location header",
			Redirector: errServer(),
			ExpError:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			var conn atomic.Value
			redirectee.OnWSConnect = func(c *websocket.Conn) {
				conn.Store(c)
			}

			// Start an OpAMP/WebSocket client.
			var connected int64
			var connectErr atomic.Value
			settings := types.StartSettings{
				Callbacks: types.CallbacksStruct{
					OnConnectFunc: func(ctx context.Context) {
						atomic.StoreInt64(&connected, 1)
					},
					OnConnectFailedFunc: func(ctx context.Context, err error) {
						if err != websocket.ErrBadHandshake {
							connectErr.Store(err)
						}
					},
				},
			}
			reURL, err := url.Parse(test.Redirector.URL)
			assert.NoError(t, err)
			reURL.Scheme = "ws"
			settings.OpAMPServerURL = reURL.String()
			client := NewWebSocket(nil)
			startClient(t, settings, client)

			// Wait for connection to be established.
			eventually(t, func() bool {
				return conn.Load() != nil || connectErr.Load() != nil || client.lastInternalErr.Load() != nil
			})
			if test.ExpError {
				if connectErr.Load() == nil && client.lastInternalErr.Load() == nil {
					t.Error("expected non-nil error")
				}
			} else {
				assert.True(t, connectErr.Load() == nil)
			}

			// Stop the client.
			err = client.Stop(context.Background())
			assert.NoError(t, err)
		})
	}
}
