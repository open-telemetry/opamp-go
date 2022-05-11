package client

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
)

const retryAfterHTTPHeader = "Retry-After"

func createAgentDescr() *protobufs.AgentDescription {
	agentDescr := &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key:   "host.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "somehost"}},
			},
		},
	}
	err := CalcHashAgentDescription(agentDescr)
	if err != nil {
		log.Fatalln(err)
	}
	return agentDescr
}

func testClients(t *testing.T, f func(client OpAMPClient)) {
	// Run the test defined by f() for WebSocket and HTTP clients.
	tests := []struct {
		name   string
		client OpAMPClient
	}{
		{
			name:   "http",
			client: NewHTTP(nil),
		},
		{
			name:   "ws",
			client: NewWebSocket(nil),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f(test.client)
		})
	}
}

func TestConnectInvalidURL(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		settings := types.StartSettings{
			OpAMPServerURL: ":not a url",
		}

		err := client.Start(context.Background(), settings)
		assert.Error(t, err)
	})
}

func eventually(t *testing.T, f func() bool) {
	assert.Eventually(t, f, 5*time.Second, 10*time.Millisecond)
}

func prepareClient(t *testing.T, settings *types.StartSettings, c OpAMPClient) {
	// Autogenerate instance id.
	entropy := ulid.Monotonic(rand.New(rand.NewSource(99)), 0)
	settings.InstanceUid = ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()

	// Make sure correct URL scheme is used, based on the type of the OpAMP client.
	u, err := url.Parse(settings.OpAMPServerURL)
	require.NoError(t, err)
	switch c.(type) {
	case *httpClient:
		u.Scheme = "http"
	case *wsClient:
		u.Scheme = "ws"
	}
	settings.OpAMPServerURL = u.String()

	settings.AgentDescription = createAgentDescr()
}

func startClient(t *testing.T, settings types.StartSettings, client OpAMPClient) {
	prepareClient(t, &settings, client)
	err := client.Start(context.Background(), settings)
	assert.NoError(t, err)
}

// Create start settings that point to a non-existing server.
func createNoServerSettings() types.StartSettings {
	return types.StartSettings{
		OpAMPServerURL: "ws://" + testhelpers.GetAvailableLocalAddress(),
	}
}

func TestConnectNoServer(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		startClient(t, createNoServerSettings(), client)
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestOnConnectFail(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		var connectErr atomic.Value
		settings := createNoServerSettings()
		settings.Callbacks = types.CallbacksStruct{
			OnConnectFailedFunc: func(err error) {
				connectErr.Store(err)
			},
		}

		startClient(t, settings, client)

		eventually(t, func() bool { return connectErr.Load() != nil })

		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestStartStarted(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		settings := createNoServerSettings()
		startClient(t, settings, client)

		// Try to start again.
		err := client.Start(context.Background(), settings)
		assert.Error(t, err)

		err = client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestStopWithoutStart(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		err := client.Stop(context.Background())
		assert.Error(t, err)
	})
}

func TestStopCancellation(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		startClient(t, createNoServerSettings(), client)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := client.Stop(ctx)
		if err != nil {
			assert.ErrorIs(t, err, context.Canceled)
		}
	})
}

func TestConnectWithServer(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		// Start a server.
		srv := internal.StartMockServer(t)

		// Start a client.
		var connected int64
		settings := types.StartSettings{
			Callbacks: types.CallbacksStruct{
				OnConnectFunc: func() {
					atomic.StoreInt64(&connected, 1)
				},
			},
		}
		settings.OpAMPServerURL = "ws://" + srv.Endpoint
		startClient(t, settings, client)

		// Wait for connection to be established.
		eventually(t, func() bool { return atomic.LoadInt64(&connected) != 0 })

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestConnectWithServer503(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
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
		settings := types.StartSettings{
			Callbacks: types.CallbacksStruct{
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
		startClient(t, settings, client)

		// Wait for connection to fail.
		eventually(t, func() bool { return connectErr.Load() != nil })

		assert.EqualValues(t, 1, atomic.LoadInt64(&connectionAttempts))
		assert.EqualValues(t, 0, atomic.LoadInt64(&clientConnected))

		// Shutdown the server.
		srv.Close()
		_ = client.Stop(context.Background())
	})
}

func TestConnectWithHeader(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		// Start a server.
		srv := internal.StartMockServer(t)
		var conn atomic.Value
		srv.OnConnect = func(r *http.Request) {
			authHdr := r.Header.Get("Authorization")
			assert.EqualValues(t, "Bearer 12345678", authHdr)
			conn.Store(true)
		}

		// Start a client.
		settings := types.StartSettings{
			OpAMPServerURL:      "ws://" + srv.Endpoint,
			AuthorizationHeader: "Bearer 12345678",
		}
		startClient(t, settings, client)

		// Wait for connection to be established.
		eventually(t, func() bool { return conn.Load() != nil })

		// Shutdown the server and the client.
		srv.Close()
		_ = client.Stop(context.Background())
	})
}

func createRemoteConfig() *protobufs.AgentRemoteConfig {
	return &protobufs.AgentRemoteConfig{
		Config: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{},
		},
		ConfigHash: []byte{1, 2, 3, 4},
	}
}

func TestFirstStatusReport(t *testing.T) {
	testClients(t, func(client OpAMPClient) {

		remoteConfig := createRemoteConfig()

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
		settings := types.StartSettings{
			Callbacks: types.CallbacksStruct{
				OnConnectFunc: func() {
					atomic.AddInt64(&connected, 1)
				},
				OnRemoteConfigFunc: func(
					ctx context.Context,
					config *protobufs.AgentRemoteConfig,
				) (
					effectiveConfig *protobufs.EffectiveConfig, configChanged bool,
					err error,
				) {
					// Verify that the client received exactly the remote config that
					// the server sent.
					assert.True(t, proto.Equal(remoteConfig, config))
					atomic.AddInt64(&remoteConfigReceived, 1)
					return &protobufs.EffectiveConfig{
						ConfigMap: remoteConfig.Config,
					}, true, nil
				},
			},
		}
		settings.OpAMPServerURL = "ws://" + srv.Endpoint
		startClient(t, settings, client)

		// Wait for connection to be established.
		eventually(t, func() bool { return atomic.LoadInt64(&connected) != 0 })

		// Wait to receive remote config.
		eventually(t, func() bool { return atomic.LoadInt64(&remoteConfigReceived) != 0 })

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
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
	settings := types.StartSettings{
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
	client := NewWebSocket(nil)
	startClient(t, settings, client)

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

func createEffectiveConfig() *protobufs.EffectiveConfig {
	cfg := &protobufs.EffectiveConfig{
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"key": {},
			},
		},
	}
	CalcHashEffectiveConfig(cfg)
	return cfg
}

func TestSetEffectiveConfig(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
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
		sendConfig := createEffectiveConfig()
		settings := types.StartSettings{
			Callbacks: types.CallbacksStruct{
				GetEffectiveConfigFunc: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
					return sendConfig, nil
				},
			},
		}
		settings.OpAMPServerURL = "ws://" + srv.Endpoint
		prepareClient(t, &settings, client)

		assert.NoError(t, client.Start(context.Background(), settings))

		// Verify config is delivered.
		eventually(
			t,
			func() bool {
				return rcvConfig.Load() != nil &&
					proto.Equal(sendConfig, rcvConfig.Load().(*protobufs.EffectiveConfig))
			},
		)

		// Now change the config.
		sendConfig.ConfigMap.ConfigMap["key2"] = &protobufs.AgentConfigFile{}
		_ = client.UpdateEffectiveConfig(context.Background())

		// Verify change is delivered.
		eventually(
			t,
			func() bool {
				return rcvConfig.Load() != nil &&
					proto.Equal(sendConfig, rcvConfig.Load().(*protobufs.EffectiveConfig))
			},
		)

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)

	})
}

func TestSetAgentDescription(t *testing.T) {
	testClients(t, func(client OpAMPClient) {

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
		settings := types.StartSettings{
			OpAMPServerURL: "ws://" + srv.Endpoint,
		}
		prepareClient(t, &settings, client)

		clientAgentDescr := createAgentDescr()
		assert.NoError(t, client.SetAgentDescription(clientAgentDescr))

		assert.NoError(t, client.Start(context.Background(), settings))

		// Verify it is delivered.
		eventually(
			t,
			func() bool {
				agentDescr, ok := rcvAgentDescr.Load().(*protobufs.AgentDescription)
				if !ok || agentDescr == nil {
					return false
				}
				return proto.Equal(clientAgentDescr, agentDescr)
			},
		)

		// Now change again.
		clientAgentDescr.NonIdentifyingAttributes = []*protobufs.KeyValue{
			{
				Key:   "os.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "linux"}},
			},
		}
		assert.NoError(t, CalcHashAgentDescription(clientAgentDescr))
		assert.NoError(t, client.SetAgentDescription(clientAgentDescr))

		// Verify change is delivered.
		eventually(
			t,
			func() bool {
				agentDescr := rcvAgentDescr.Load().(*protobufs.AgentDescription)
				if agentDescr == nil {
					return false
				}
				return proto.Equal(clientAgentDescr, agentDescr)
			},
		)

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestAgentIdentification(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
		// Start a server.
		srv := internal.StartMockServer(t)
		newInstanceUid := ulid.MustNew(
			ulid.Timestamp(time.Now()), ulid.Monotonic(rand.New(rand.NewSource(0)), 0),
		)
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
		settings := types.StartSettings{}
		settings.OpAMPServerURL = "ws://" + srv.Endpoint
		settings.AgentDescription = createAgentDescr()
		settings.Callbacks = types.CallbacksStruct{
			OnAgentIdentificationFunc: func(
				ctx context.Context,
				agentId *protobufs.AgentIdentification,
			) error {
				return nil
			},
		}
		prepareClient(t, &settings, client)

		oldInstanceUid := settings.InstanceUid
		assert.NoError(t, client.Start(context.Background(), settings))

		// First, server gets the original instanceId
		eventually(
			t,
			func() bool {
				instanceUid, ok := rcvAgentInstanceUid.Load().(string)
				if !ok {
					return false
				}
				return instanceUid == oldInstanceUid
			},
		)

		// Send a dummy message
		_ = client.SetAgentDescription(createAgentDescr())

		// When it was sent, the new instance uid should have been used, which should
		// have been observed by the server
		eventually(
			t,
			func() bool {
				instanceUid, ok := rcvAgentInstanceUid.Load().(string)
				if !ok {
					return false
				}
				return instanceUid == newInstanceUid.String()
			},
		)

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestConnectionSettings(t *testing.T) {
	testClients(t, func(client OpAMPClient) {
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
		settings := types.StartSettings{
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
					ctx context.Context, name string,
					settings *protobufs.ConnectionSettings,
				) error {
					assert.EqualValues(t, "other", name)
					assert.True(t, proto.Equal(otherSettings, settings))
					atomic.AddInt64(&gotOtherSettings, 1)
					return nil
				},
			},
		}
		settings.OpAMPServerURL = "ws://" + srv.Endpoint
		prepareClient(t, &settings, client)

		assert.NoError(t, client.Start(context.Background(), settings))

		eventually(t, func() bool { return atomic.LoadInt64(&gotOpampSettings) == 1 })
		eventually(t, func() bool { return atomic.LoadInt64(&gotOwnSettings) == 3 })
		eventually(t, func() bool { return atomic.LoadInt64(&gotOtherSettings) == 1 })
		eventually(t, func() bool { return atomic.LoadInt64(&rcvStatus) == 1 })

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestReportAgentDescription(t *testing.T) {
	testClients(t, func(client OpAMPClient) {

		// Start a server.
		srv := internal.StartMockServer(t)
		srv.EnableExpectMode()

		// Start a client.
		settings := types.StartSettings{
			OpAMPServerURL: "ws://" + srv.Endpoint,
		}
		prepareClient(t, &settings, client)

		// client --->
		assert.NoError(t, client.Start(context.Background(), settings))

		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// The first status report after Start must have full AgentDescription.
			assert.True(t, proto.Equal(settings.AgentDescription, msg.StatusReport.AgentDescription))
			return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
		})

		// client --->
		// Trigger a status report.
		_ = client.UpdateEffectiveConfig(context.Background())

		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// The status report must have compressed AgentDescription.
			descr := msg.StatusReport.AgentDescription
			assert.Nil(t, descr.IdentifyingAttributes)
			assert.Nil(t, descr.NonIdentifyingAttributes)

			// The Hash field must be present and unchanged.
			assert.NotNil(t, descr.Hash)
			assert.EqualValues(t, settings.AgentDescription.Hash, descr.Hash)

			// Ask client for full AgentDescription.
			return &protobufs.ServerToAgent{
				InstanceUid: msg.InstanceUid,
				Flags:       protobufs.ServerToAgent_ReportAgentDescription,
			}
		})

		// Server has requested the client to report, so there will be another message
		// coming to the Server.
		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// The status report must again have full AgentDescription
			// because the server asked for it.
			assert.True(t, proto.Equal(settings.AgentDescription, msg.StatusReport.AgentDescription))
			return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
		})

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestReportEffectiveConfig(t *testing.T) {
	testClients(t, func(client OpAMPClient) {

		// Start a server.
		srv := internal.StartMockServer(t)
		srv.EnableExpectMode()

		clientEffectiveConfig := createEffectiveConfig()

		// Start a client.
		settings := types.StartSettings{
			OpAMPServerURL: "ws://" + srv.Endpoint,
			Callbacks: types.CallbacksStruct{
				GetEffectiveConfigFunc: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
					return clientEffectiveConfig, nil
				},
			},
		}
		prepareClient(t, &settings, client)

		// client --->
		assert.NoError(t, client.Start(context.Background(), settings))

		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// The first status report after Start must have full EffectiveConfig.
			assert.True(t, proto.Equal(clientEffectiveConfig, msg.StatusReport.EffectiveConfig))
			return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
		})

		// client --->
		// Trigger another status report for example by setting AgentDescription.
		_ = client.SetAgentDescription(settings.AgentDescription)

		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// The status report must have compressed EffectiveConfig.
			cfg := msg.StatusReport.EffectiveConfig
			assert.Nil(t, cfg.ConfigMap)

			// Hash must be present and unchanged.
			assert.NotNil(t, cfg.Hash)
			assert.EqualValues(t, clientEffectiveConfig.Hash, cfg.Hash)

			// Ask client for full AgentDescription.
			return &protobufs.ServerToAgent{
				InstanceUid: msg.InstanceUid,
				Flags:       protobufs.ServerToAgent_ReportEffectiveConfig,
			}
		})

		// Server has requested the client to report, so there will be another message.
		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// The status report must again have full EffectiveConfig
			// because server asked for it.
			assert.True(t, proto.Equal(clientEffectiveConfig, msg.StatusReport.EffectiveConfig))
			return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
		})

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func verifyRemoteConfigUpdate(t *testing.T, successCase bool, expectStatus *protobufs.RemoteConfigStatus) {
	testClients(t, func(client OpAMPClient) {

		// Start a server.
		srv := internal.StartMockServer(t)
		srv.EnableExpectMode()

		// Prepare a callback that returns either success or failure.
		onRemoteConfigFunc := func(
			ctx context.Context, remoteConfig *protobufs.AgentRemoteConfig,
		) (effectiveConfig *protobufs.EffectiveConfig, configChanged bool, err error) {
			if successCase {
				return createEffectiveConfig(), true, nil
			} else {
				return nil, false, errors.New("cannot update remote config")
			}
		}

		// Start a client.
		settings := types.StartSettings{
			OpAMPServerURL: "ws://" + srv.Endpoint,
			Callbacks: types.CallbacksStruct{
				OnRemoteConfigFunc: onRemoteConfigFunc,
			},
		}
		prepareClient(t, &settings, client)

		// client --->
		assert.NoError(t, client.Start(context.Background(), settings))

		remoteCfg := createRemoteConfig()
		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// Send the remote config to the agent.
			return &protobufs.ServerToAgent{
				InstanceUid:  msg.InstanceUid,
				RemoteConfig: remoteCfg,
			}
		})

		// The agent will try to apply the remote config and will send the status
		// report about it back to the server.

		var firstConfigStatus *protobufs.RemoteConfigStatus

		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// Verify that the remote config status is as expected.
			status := msg.StatusReport.RemoteConfigStatus
			assert.EqualValues(t, expectStatus.Status, status.Status)
			assert.Equal(t, expectStatus.ErrorMessage, status.ErrorMessage)
			assert.EqualValues(t, remoteCfg.ConfigHash, status.LastRemoteConfigHash)
			assert.NotNil(t, status.Hash)

			firstConfigStatus = proto.Clone(status).(*protobufs.RemoteConfigStatus)

			return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
		})

		// client --->
		// Trigger another status report by setting AgentDescription.
		_ = client.SetAgentDescription(settings.AgentDescription)

		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// This time all fields except Hash must be unset. This is expected
			// as compression in OpAMP.
			status := msg.StatusReport.RemoteConfigStatus
			assert.EqualValues(t, firstConfigStatus.Hash, status.Hash)
			assert.EqualValues(t, protobufs.RemoteConfigStatus_UNSET, status.Status)
			assert.EqualValues(t, "", status.ErrorMessage)
			assert.Nil(t, status.LastRemoteConfigHash)

			return &protobufs.ServerToAgent{
				InstanceUid: msg.InstanceUid,
				// Ask client to report full status.
				Flags: protobufs.ServerToAgent_ReportRemoteConfigStatus,
			}
		})

		// ---> server
		srv.Expect(func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
			// Exact same full status must be present again.
			status := msg.StatusReport.RemoteConfigStatus
			assert.True(t, proto.Equal(status, firstConfigStatus))

			return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
		})

		// Shutdown the server.
		srv.Close()

		// Shutdown the client.
		err := client.Stop(context.Background())
		assert.NoError(t, err)
	})
}

func TestRemoteConfigUpdate(t *testing.T) {

	tests := []struct {
		name           string
		success        bool
		expectedStatus *protobufs.RemoteConfigStatus
	}{
		{
			name:    "success",
			success: true,
			expectedStatus: &protobufs.RemoteConfigStatus{
				Status:       protobufs.RemoteConfigStatus_APPLIED,
				ErrorMessage: "",
			},
		},
		{
			name:    "fail",
			success: false,
			expectedStatus: &protobufs.RemoteConfigStatus{
				Status:       protobufs.RemoteConfigStatus_FAILED,
				ErrorMessage: "cannot update remote config",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifyRemoteConfigUpdate(t, test.success, test.expectedStatus)
		})
	}
}
