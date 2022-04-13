package client

import (
	"context"
	"math/rand"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	ulid "github.com/oklog/ulid/v2"
	"github.com/open-telemetry/opamp-go/protobufshelpers"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
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

func prepareClient(settings StartSettings) *client {
	// Autogenerate instance id.
	entropy := ulid.Monotonic(rand.New(rand.NewSource(99)), 0)
	settings.InstanceUid = ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()

	return New(nil)
}

func startClient(t *testing.T, settings StartSettings) *client {
	client := prepareClient(settings)
	err := client.Start(settings)
	assert.NoError(t, err)
	return client
}

// Create start settings that point to a non-existing server.
func createNoServerSettings() StartSettings {
	return StartSettings{
		OpAMPServerURL:   "ws://" + testhelpers.GetAvailableLocalAddress(),
		AgentDescription: &protobufs.AgentDescription{},
	}
}

func TestConnectNoServer(t *testing.T) {
	client := startClient(t, createNoServerSettings())

	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestOnConnectFail(t *testing.T) {
	var connectErr atomic.Value
	settings := createNoServerSettings()
	settings.Callbacks = types.CallbacksStruct{
		OnConnectFailedFunc: func(err error) {
			connectErr.Store(err)
		},
	}

	client := startClient(t, settings)

	eventually(t, func() bool { return connectErr.Load() != nil })

	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestStartStarted(t *testing.T) {
	settings := createNoServerSettings()
	client := startClient(t, settings)

	// Try to start again.
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
	client := startClient(t, createNoServerSettings())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.Stop(ctx)
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	}
}

func TestConnectWithServer(t *testing.T) {
	// Start a server.
	srv := internal.StartMockServer(t)

	// Start a client.
	var connected int64
	settings := StartSettings{
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.StoreInt64(&connected, 1)
			},
		},
		AgentDescription: &protobufs.AgentDescription{},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	// Wait for connection to be established.
	eventually(t, func() bool { return atomic.LoadInt64(&connected) != 0 })

	// Shutdown the server.
	srv.Close()

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
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.StoreInt64(&clientConnected, 1)
				assert.Fail(t, "Client should not be able to connect")
			},
			OnConnectFailedFunc: func(err error) {
				connectErr.Store(err)
			},
		},
		AgentDescription: &protobufs.AgentDescription{},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	// Wait for connection to fail.
	eventually(t, func() bool { return connectErr.Load() != nil })

	assert.EqualValues(t, 1, atomic.LoadInt64(&connectionAttempts))
	assert.EqualValues(t, 0, atomic.LoadInt64(&clientConnected))

	// Shutdown the server.
	srv.Close()
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
		AgentDescription:    &protobufs.AgentDescription{},
	}
	client := startClient(t, settings)

	// Wait for connection to be established.
	eventually(t, func() bool { return conn.Load() != nil })

	// Shutdown the server and the client.
	srv.Close()
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
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.StoreInt64(&connected, 1)
			},
			OnConnectFailedFunc: func(err error) {
				connectErr.Store(err)
			},
		},
		AgentDescription: &protobufs.AgentDescription{},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	// Wait for connection to be established.
	eventually(t, func() bool { return conn.Load() != nil })
	assert.True(t, connectErr.Load() == nil)

	// Close the server and forcefully disconnect.
	srv.Close()
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
			InstanceUid:  msg.InstanceUid,
			RemoteConfig: remoteConfig,
		}
	}

	// Start a client.
	var connected, remoteConfigReceived int64
	settings := StartSettings{
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.AddInt64(&connected, 1)
			},
			OnRemoteConfigFunc: func(
				ctx context.Context,
				config *protobufs.AgentRemoteConfig,
			) (effectiveConfig *protobufs.EffectiveConfig, configChanged bool, err error) {
				// Verify that the client received exactly the remote config that
				// the server sent.
				assert.True(t, proto.Equal(remoteConfig, config))
				atomic.AddInt64(&remoteConfigReceived, 1)
				return &protobufs.EffectiveConfig{
					ConfigMap: remoteConfig.Config,
				}, true, nil
			},
		},
		AgentDescription: &protobufs.AgentDescription{},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	// Wait for connection to be established.
	eventually(t, func() bool { return atomic.LoadInt64(&connected) != 0 })

	// Wait to receive remote config.
	eventually(t, func() bool { return atomic.LoadInt64(&remoteConfigReceived) != 0 })

	// Shutdown the server.
	srv.Close()

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestIncludesDetailsOnReconnect(t *testing.T) {
	srv := internal.StartMockServer(t)

	var receivedDetails int64
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		// Track when we receive AgentDescription
		if msg.StatusReport.AgentDescription != nil {
			atomic.AddInt64(&receivedDetails, 1)
		}

		return &protobufs.ServerToAgent{
			InstanceUid: msg.InstanceUid,
		}
	}

	var connected int64
	settings := StartSettings{
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func() {
				atomic.AddInt64(&connected, 1)
			},
			OnRemoteConfigFunc: func(
				ctx context.Context,
				config *protobufs.AgentRemoteConfig,
			) (effectiveConfig *protobufs.EffectiveConfig, configChanged bool, err error) {
				return &protobufs.EffectiveConfig{}, false, nil
			},
		},
		AgentDescription: &protobufs.AgentDescription{},
	}

	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := startClient(t, settings)

	eventually(t, func() bool { return atomic.LoadInt64(&connected) == 1 })
	eventually(t, func() bool { return atomic.LoadInt64(&receivedDetails) == 1 })

	// close the agent connection. expect it to reconnect and send details again.
	err := client.conn.Close()
	assert.NoError(t, err)

	eventually(t, func() bool { return atomic.LoadInt64(&connected) == 2 })
	eventually(t, func() bool { return atomic.LoadInt64(&receivedDetails) == 2 })

	err = client.Stop(context.Background())
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
	settings.AgentDescription = &protobufs.AgentDescription{}
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
	srv.Close()

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func isEqualAgentAttrs(sent []*protobufs.KeyValue, received []*protobufs.KeyValue) bool {
	if len(sent) != len(received) {
		return false
	}

	for i, rval := range received {
		sval := sent[i]
		if !protobufshelpers.IsEqualKeyValue(rval, sval) {
			return false
		}
	}

	return true
}

func TestSetAgentAttributes(t *testing.T) {
	// Start a server.
	srv := internal.StartMockServer(t)
	var rcvAgentDescr atomic.Value
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		if statusReport := msg.GetStatusReport(); statusReport != nil {
			if statusReport.AgentDescription != nil {
				rcvAgentDescr.Store(statusReport.AgentDescription)
			}
		}
		return nil
	}

	// Start a client.
	settings := StartSettings{
		OpAMPServerURL:   "ws://" + srv.Endpoint,
		AgentDescription: &protobufs.AgentDescription{},
	}
	client := prepareClient(settings)

	settings.AgentDescription = &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key:   "host.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "somehost"}},
			},
		},
	}

	assert.NoError(t, client.Start(settings))

	// Verify it is delivered.
	eventually(t, func() bool {
		agentDescr, ok := rcvAgentDescr.Load().(*protobufs.AgentDescription)
		if !ok || agentDescr == nil {
			return false
		}
		return isEqualAgentAttrs(settings.AgentDescription.IdentifyingAttributes, agentDescr.IdentifyingAttributes)
	})

	// Now change again.
	settings.AgentDescription.NonIdentifyingAttributes = []*protobufs.KeyValue{
		{
			Key:   "os.name",
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "linux"}},
		},
	}

	client.SetAgentDescription(settings.AgentDescription)

	// Verify change is delivered.
	eventually(t, func() bool {
		agentDescr := rcvAgentDescr.Load().(*protobufs.AgentDescription)
		if agentDescr == nil {
			return false
		}
		return isEqualAgentAttrs(settings.AgentDescription.IdentifyingAttributes, agentDescr.IdentifyingAttributes) &&
			isEqualAgentAttrs(settings.AgentDescription.NonIdentifyingAttributes, agentDescr.NonIdentifyingAttributes)
	})

	// Shutdown the server.
	srv.Close()

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestAgentIdentification(t *testing.T) {
	// Start a server.
	srv := internal.StartMockServer(t)
	newInstanceUid := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.Monotonic(rand.New(rand.NewSource(0)), 0))
	var rcvAgentInstanceUid atomic.Value
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		rcvAgentInstanceUid.Store(msg.InstanceUid)
		return &protobufs.ServerToAgent{
			InstanceUid: msg.InstanceUid,
			AgentIdentification: &protobufs.AgentIdentification{
				NewInstanceUid: newInstanceUid.String(),
			},
		}
	}

	// Start a client.
	settings := StartSettings{}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	settings.AgentDescription = &protobufs.AgentDescription{}
	settings.Callbacks = types.CallbacksStruct{
		OnAgentIdentificationFunc: func(
			ctx context.Context,
			agentId *protobufs.AgentIdentification,
		) error {
			return nil
		},
	}
	client := prepareClient(settings)

	oldInstanceUid := settings.InstanceUid
	assert.NoError(t, client.Start(settings))

	// First, server gets the original instanceId
	eventually(t, func() bool {
		instanceUid, ok := rcvAgentInstanceUid.Load().(string)
		if !ok {
			return false
		}
		return instanceUid == oldInstanceUid
	})

	// Send a dummy message
	client.SetAgentDescription(&protobufs.AgentDescription{})

	// When it was sent, the new instance uid should have been used, which should
	// have been observed by the server
	eventually(t, func() bool {
		instanceUid, ok := rcvAgentInstanceUid.Load().(string)
		if !ok {
			return false
		}
		return instanceUid == newInstanceUid.String()
	})

	// Shutdown the server.
	srv.Close()

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
			}
		}
		return nil
	}

	var gotOpampSettings int64
	var gotOwnSettings int64
	var gotOtherSettings int64

	// Start a client.
	settings := StartSettings{
		Callbacks: types.CallbacksStruct{
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
		AgentDescription: &protobufs.AgentDescription{},
	}
	settings.OpAMPServerURL = "ws://" + srv.Endpoint
	client := prepareClient(settings)

	assert.NoError(t, client.Start(settings))

	eventually(t, func() bool { return atomic.LoadInt64(&gotOpampSettings) == 1 })
	eventually(t, func() bool { return atomic.LoadInt64(&gotOwnSettings) == 3 })
	eventually(t, func() bool { return atomic.LoadInt64(&gotOtherSettings) == 1 })
	eventually(t, func() bool { return atomic.LoadInt64(&rcvStatus) == 1 })

	// Shutdown the server.
	srv.Close()

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}
