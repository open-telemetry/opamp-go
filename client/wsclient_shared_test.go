package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func redirectServer(to string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, to, status)
	}))
}

func errServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(302)
	}))
}

type checkRedirectMock struct {
	mock.Mock
	t      testing.TB
	viaLen int
	http   bool
}

func (c *checkRedirectMock) CheckRedirect(req *http.Request, viaReq []*http.Request, via []*http.Response) error {
	if req == nil {
		c.t.Error("nil request in CheckRedirect")
		return errors.New("nil request in CheckRedirect")
	}
	if len(viaReq) > c.viaLen {
		c.t.Error("viaReq should be shorter than viaLen")
	}
	if !c.http {
		// websocket transport
		if len(via) > c.viaLen {
			c.t.Error("via should be shorter than viaLen")
		}
	}
	if !c.http && len(via) > 0 {
		location, err := via[len(via)-1].Location()
		if err != nil {
			c.t.Error(err)
		}
		// the URL of the request should match the location header of the last response
		assert.Equal(c.t, req.URL, location, "request URL should equal the location in the response")
	}
	return c.Called(req, via).Error(0)
}

func mockRedirect(t testing.TB, viaLen int, err error) *checkRedirectMock {
	m := &checkRedirectMock{
		t:      t,
		viaLen: viaLen,
	}
	m.On("CheckRedirect", mock.Anything, mock.Anything, mock.Anything).Return(err)
	return m
}

func TestHandlesStopBeforeStart(t *testing.T) {
	client := NewWebSocket(nil)
	require.Error(t, client.Stop(context.Background()))
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
				Callbacks: types.Callbacks{
					OnMessage: func(ctx context.Context, msg *types.MessageData) {
						if msg.RemoteConfig != nil {
							clientGotRemoteConfig.Store(msg.RemoteConfig)
						}
					},
					GetEffectiveConfig: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
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
						"": {
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
			var stopWg sync.WaitGroup
			stopWg.Add(1)

			go func() {
				defer stopWg.Done()

				// client.Stop() should send an AgentDisconnect message to the server.
				// because we are using Expect mode, we should stop asynchronously
				err := client.Stop(context.Background())
				assert.NoError(t, err)
			}()
			srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
				return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
			})
			stopWg.Wait()

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

func TestWSClientStopSendAgentDisconnectMessage(t *testing.T) {
	srv := internal.StartMockServer(t)
	srv.EnableExpectMode()

	client := NewWebSocket(nil)
	client.connShutdownTimeout = 100 * time.Millisecond
	startClient(t, types.StartSettings{
		OpAMPServerURL: srv.GetHTTPTestServer().URL,
	}, client)

	// Wait for connection to be established.
	srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
	})

	var stopWg sync.WaitGroup
	stopWg.Add(1)
	go func() {
		defer stopWg.Done()
		client.Stop(context.Background())
	}()

	srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		assert.NotNil(t, msg.AgentDisconnect)
		return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
	})

	stopWg.Wait()
}
