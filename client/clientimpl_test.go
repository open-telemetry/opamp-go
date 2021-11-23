package client

import (
	"context"
	"math/rand"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/protobufs"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
)

func TestConnectInvalidURL(t *testing.T) {
	settings := StartSettings{
		OpAMPServerURL: ":not a url",
	}

	client := New(nil)
	err := client.Start(settings)
	assert.Error(t, err)
}

func eventually(t *testing.T, f func() bool) {
	assert.Eventually(t, f, 5*time.Second, 10*time.Millisecond)
}

func prepareClient(settings StartSettings) *Impl {
	// Autogenerate instance id.
	entropy := ulid.Monotonic(rand.New(rand.NewSource(99)), 0)
	settings.InstanceUid = ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()

	return New(nil)
}

func startClient(t *testing.T, settings StartSettings) *Impl {
	client := prepareClient(settings)
	err := client.Start(settings)
	assert.NoError(t, err)
	return client
}

func TestConnectNoServer(t *testing.T) {
	settings := StartSettings{
		OpAMPServerURL: "ws://" + testhelpers.GetAvailableLocalAddress(),
	}

	client := startClient(t, settings)

	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestOnConnectFail(t *testing.T) {
	var connectErr atomic.Value
	settings := StartSettings{
		OpAMPServerURL: "ws://" + testhelpers.GetAvailableLocalAddress(),
		Callbacks: internal.CallbacksStruct{
			OnConnectFailedFunc: func(err error) {
				connectErr.Store(err)
			},
		},
	}

	client := startClient(t, settings)

	eventually(t, func() bool { return connectErr.Load() != nil })

	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestStartStarted(t *testing.T) {
	settings := StartSettings{
		OpAMPServerURL: "ws://" + testhelpers.GetAvailableLocalAddress(),
	}

	client := startClient(t, settings)

	err := client.Start(settings)
	assert.Error(t, err)

	err = client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestStopWithoutStart(t *testing.T) {
	client := New(nil)
	err := client.Stop(context.Background())
	assert.Error(t, err)
}

func TestStopCancellation(t *testing.T) {
	settings := StartSettings{
		OpAMPServerURL: "ws://" + testhelpers.GetAvailableLocalAddress(),
	}
	client := startClient(t, settings)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.Stop(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestConnectWithServer(t *testing.T) {
	// Start a server.
	srv := internal.StartMockServer(t)

	// Start a client.
	var connected int64
	settings := StartSettings{
		Callbacks: internal.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.StoreInt64(&connected, 1)
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	// Wait for connection to be established.
	eventually(t, func() bool { return atomic.LoadInt64(&connected) != 0 })

	// Shutdown the server.
	srv.HTTPSrv.Shutdown(context.Background())

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestConnectWithServer503(t *testing.T) {
	// Start a server.
	var connectionAttempts int64
	srv := internal.StartMockServer(t)
	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt64(&connectionAttempts, 1)

		// Always respond with an error to the client.
		w.Header().Set(retryAfterHTTPHeader, "30")
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	// Start a client.
	var clientConnected int64
	var connectErr atomic.Value
	settings := StartSettings{
		Callbacks: internal.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.StoreInt64(&clientConnected, 1)
				assert.Fail(t, "Client should not be able to connect")
			},
			OnConnectFailedFunc: func(err error) {
				connectErr.Store(err)
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	// Wait for connection to fail.
	eventually(t, func() bool { return connectErr.Load() != nil })

	assert.EqualValues(t, 1, atomic.LoadInt64(&connectionAttempts))
	assert.EqualValues(t, 0, atomic.LoadInt64(&clientConnected))

	// Shutdown the server.
	srv.HTTPSrv.Shutdown(context.Background())
	client.Stop(context.Background())
}

func TestConnectWithHeader(t *testing.T) {
	// Start a server.
	srv := internal.StartMockServer(t)
	var conn atomic.Value
	srv.OnConnect = func(r *http.Request, c *websocket.Conn) {
		authHdr := r.Header.Get("Authorization")
		assert.EqualValues(t, "Bearer 12345678", authHdr)
		conn.Store(c)
	}

	// Start a client.
	settings := StartSettings{
		OpAMPServerURL:      "ws://" + srv.Endpoint,
		AuthorizationHeader: "Bearer 12345678",
	}
	client := startClient(t, settings)

	// Wait for connection to be established.
	eventually(t, func() bool { return conn.Load() != nil })

	// Shutdown the server and the client.
	srv.HTTPSrv.Shutdown(context.Background())
	client.Stop(context.Background())
}

func TestDisconnectByServer(t *testing.T) {
	// Start a server.
	srv := internal.StartMockServer(t)
	var conn atomic.Value
	srv.OnConnect = func(r *http.Request, c *websocket.Conn) {
		conn.Store(c)
	}

	// Start a client.
	var connected int64
	var connectErr atomic.Value
	settings := StartSettings{
		Callbacks: internal.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.StoreInt64(&connected, 1)
			},
			OnConnectFailedFunc: func(err error) {
				connectErr.Store(err)
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	// Wait for connection to be established.
	eventually(t, func() bool { return conn.Load() != nil })
	assert.True(t, connectErr.Load() == nil)

	// Close the server and forcefully disconnect.
	srv.HTTPSrv.Close()
	conn.Load().(*websocket.Conn).Close()

	// The client must retry and must fail now.
	eventually(t, func() bool { return connectErr.Load() != nil })

	// Stop the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestFirstStatusReport(t *testing.T) {
	remoteConfig := &protobufs.AgentRemoteConfig{
		Config: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{},
		},
		ConfigHash: []byte{1, 2, 3, 4},
	}

	// Start a server.
	srv := internal.StartMockServer(t)
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		return &protobufs.ServerToAgent{
			InstanceUid: msg.InstanceUid,
			Body: &protobufs.ServerToAgent_DataForAgent{
				DataForAgent: &protobufs.DataForAgent{
					RemoteConfig: remoteConfig,
				},
			},
		}
	}

	// Start a client.
	var connected, remoteConfigReceived int64
	settings := StartSettings{
		Callbacks: internal.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.AddInt64(&connected, 1)
			},
			OnRemoteConfigFunc: func(
				ctx context.Context, config *protobufs.AgentRemoteConfig,
			) (*protobufs.EffectiveConfig, error) {
				// Verify that the client received exactly the remote config that
				// the server sent.
				assert.True(t, proto.Equal(remoteConfig, config))
				atomic.AddInt64(&remoteConfigReceived, 1)
				return &protobufs.EffectiveConfig{
					ConfigMap: remoteConfig.Config,
				}, nil
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	// Wait for connection to be established.
	eventually(t, func() bool { return atomic.LoadInt64(&connected) != 0 })

	// Wait to receive remote config.
	eventually(t, func() bool { return atomic.LoadInt64(&remoteConfigReceived) != 0 })

	// Shutdown the server.
	srv.HTTPSrv.Shutdown(context.Background())

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestSetEffectiveConfig(t *testing.T) {
	// Start a server.
	srv := internal.StartMockServer(t)
	var rcvConfig atomic.Value
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		if statusReport := msg.GetStatusReport(); statusReport != nil {
			if statusReport.EffectiveConfig != nil {
				rcvConfig.Store(statusReport.EffectiveConfig)
			}
		}
		return nil
	}

	// Start a client.
	settings := StartSettings{}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := prepareClient(settings)

	sendConfig := &protobufs.EffectiveConfig{
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"key": {},
			},
		},
	}

	// Set before start. It should be delivered immediately after Start().
	client.SetEffectiveConfig(sendConfig)

	assert.NoError(t, client.Start(settings))

	// Verify it is delivered.
	eventually(t, func() bool { return proto.Equal(sendConfig, rcvConfig.Load().(*protobufs.EffectiveConfig)) })

	// Now change again.
	sendConfig.ConfigMap.ConfigMap["key2"] = &protobufs.AgentConfigFile{}
	client.SetEffectiveConfig(sendConfig)

	// Verify change is delivered.
	eventually(t, func() bool { return proto.Equal(sendConfig, rcvConfig.Load().(*protobufs.EffectiveConfig)) })

	// Shutdown the server.
	srv.HTTPSrv.Shutdown(context.Background())

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestConnectionSettings(t *testing.T) {
	hash := []byte{1, 2, 3}
	opampSettings := &protobufs.ConnectionSettings{DestinationEndpoint: "http://opamp.com"}
	metricsSettings := &protobufs.ConnectionSettings{DestinationEndpoint: "http://metrics.com"}
	tracesSettings := &protobufs.ConnectionSettings{DestinationEndpoint: "http://traces.com"}
	logsSettings := &protobufs.ConnectionSettings{DestinationEndpoint: "http://logs.com"}
	otherSettings := &protobufs.ConnectionSettings{DestinationEndpoint: "http://other.com"}

	var rcvStatus int64
	// Start a server.
	srv := internal.StartMockServer(t)
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		if statusReport := msg.GetStatusReport(); statusReport != nil {
			atomic.AddInt64(&rcvStatus, 1)

			return &protobufs.ServerToAgent{
				Body: &protobufs.ServerToAgent_DataForAgent{
					DataForAgent: &protobufs.DataForAgent{
						ConnectionSettings: &protobufs.ConnectionSettingsOffers{
							Hash:       hash,
							Opamp:      opampSettings,
							OwnMetrics: metricsSettings,
							OwnTraces:  tracesSettings,
							OwnLogs:    logsSettings,
							OtherConnections: map[string]*protobufs.ConnectionSettings{
								"other": otherSettings,
							},
						},
					},
				},
			}
		}
		return nil
	}

	var gotOpampSettings int64
	var gotOwnSettings int64
	var gotOtherSettings int64

	// Start a client.
	settings := StartSettings{
		Callbacks: internal.CallbacksStruct{
			OnOpampConnectionSettingsFunc: func(
				ctx context.Context, settings *protobufs.ConnectionSettings,
			) error {
				assert.True(t, proto.Equal(opampSettings, settings))
				atomic.AddInt64(&gotOpampSettings, 1)
				return nil
			},

			OnOwnTelemetryConnectionSettingsFunc: func(
				ctx context.Context, telemetryType types.OwnTelemetryType,
				settings *protobufs.ConnectionSettings,
			) error {
				switch telemetryType {
				case types.OwnMetrics:
					assert.True(t, proto.Equal(metricsSettings, settings))
				case types.OwnTraces:
					assert.True(t, proto.Equal(tracesSettings, settings))
				case types.OwnLogs:
					assert.True(t, proto.Equal(logsSettings, settings))
				}
				atomic.AddInt64(&gotOwnSettings, 1)
				return nil
			},

			OnOtherConnectionSettingsFunc: func(
				ctx context.Context, name string, settings *protobufs.ConnectionSettings,
			) error {
				assert.EqualValues(t, "other", name)
				assert.True(t, proto.Equal(otherSettings, settings))
				atomic.AddInt64(&gotOtherSettings, 1)
				return nil
			},
		},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := prepareClient(settings)

	assert.NoError(t, client.Start(settings))

	eventually(t, func() bool { return atomic.LoadInt64(&gotOpampSettings) == 1 })
	eventually(t, func() bool { return atomic.LoadInt64(&gotOwnSettings) == 3 })
	eventually(t, func() bool { return atomic.LoadInt64(&gotOtherSettings) == 1 })
	eventually(t, func() bool { return atomic.LoadInt64(&rcvStatus) == 1 })

	// Shutdown the server.
	srv.HTTPSrv.Shutdown(context.Background())

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}
