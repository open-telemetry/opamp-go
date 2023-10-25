package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

func startServer(t *testing.T, settings *StartSettings) *server {
	srv := New(&sharedinternal.NopLogger{})
	require.NotNil(t, srv)
	if settings.ListenEndpoint == "" {
		// Find an avaiable port to listne on.
		settings.ListenEndpoint = testhelpers.GetAvailableLocalAddress()
	}
	if settings.ListenPath == "" {
		settings.ListenPath = "/"
	}
	err := srv.Start(*settings)
	require.NoError(t, err)

	return srv
}

func dialClient(serverSettings *StartSettings) (*websocket.Conn, *http.Response, error) {
	srvUrl := "ws://" + serverSettings.ListenEndpoint + serverSettings.ListenPath
	dailer := websocket.DefaultDialer
	dailer.EnableCompression = serverSettings.EnableCompression
	return websocket.DefaultDialer.Dial(srvUrl, nil)
}

func TestServerStartStop(t *testing.T) {
	srv := startServer(t, &StartSettings{})

	err := srv.Start(StartSettings{})
	assert.ErrorIs(t, err, errAlreadyStarted)

	err = srv.Stop(context.Background())
	assert.NoError(t, err)
}

func TestServerAddrWithNonZeroPort(t *testing.T) {
	srv := New(&sharedinternal.NopLogger{})
	require.NotNil(t, srv)

	// Nil if not started
	assert.Nil(t, srv.Addr())

	addr := testhelpers.GetAvailableLocalAddress()

	err := srv.Start(StartSettings{
		ListenEndpoint: addr,
		ListenPath:     "/",
	})
	assert.NoError(t, err)

	assert.Equal(t, addr, srv.Addr().String())

	err = srv.Stop(context.Background())
	assert.NoError(t, err)
}

func TestServerAddrWithZeroPort(t *testing.T) {
	srv := New(&sharedinternal.NopLogger{})
	require.NotNil(t, srv)

	// Nil if not started
	assert.Nil(t, srv.Addr())

	err := srv.Start(StartSettings{
		ListenEndpoint: "127.0.0.1:0",
		ListenPath:     "/",
	})
	assert.NoError(t, err)

	// should be listening on an non-zero ephemeral port
	assert.NotEqual(t, "127.0.0.1:0", srv.Addr().String())
	assert.Regexp(t, `^127.0.0.1:\d+`, srv.Addr().String())

	err = srv.Stop(context.Background())
	assert.NoError(t, err)
}

func TestServerStartRejectConnection(t *testing.T) {
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			// Reject the incoming HTTP connection.
			return types.ConnectionResponse{
				Accept:             false,
				HTTPStatusCode:     503,
				HTTPResponseHeader: map[string]string{"Retry-After": "30"},
			}
		},
	}

	// Start a Server.
	settings := &StartSettings{Settings: Settings{Callbacks: callbacks}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Try to connect to the Server.
	conn, resp, err := dialClient(settings)

	// Verify that the connection is rejected and rejection data is available to the client.
	assert.Nil(t, conn)
	assert.Error(t, err)
	assert.EqualValues(t, 503, resp.StatusCode)
	assert.EqualValues(t, "30", resp.Header.Get("Retry-After"))
}

func eventually(t *testing.T, f func() bool) {
	assert.Eventually(t, f, 5*time.Second, 10*time.Millisecond)
}

func TestServerStartAcceptConnection(t *testing.T) {
	connectedCalled := int32(0)
	connectionCloseCalled := int32(0)
	var srvConn types.Connection
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectedFunc: func(ctx context.Context, conn types.Connection) {
					srvConn = conn
					atomic.StoreInt32(&connectedCalled, 1)
				},
				OnConnectionCloseFunc: func(conn types.Connection) {
					atomic.StoreInt32(&connectionCloseCalled, 1)
					assert.EqualValues(t, srvConn, conn)
				},
			}}
		},
	}

	// Start a Server.
	settings := &StartSettings{Settings: Settings{Callbacks: callbacks}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Connect to the Server.
	conn, resp, err := dialClient(settings)

	// Verify that the connection is successful.
	assert.NoError(t, err)
	assert.NotNil(t, conn)
	require.NotNil(t, resp)
	assert.EqualValues(t, 101, resp.StatusCode)
	eventually(t, func() bool { return atomic.LoadInt32(&connectedCalled) == 1 })
	assert.True(t, atomic.LoadInt32(&connectionCloseCalled) == 0)

	// Verify that the RemoteAddr is correct.
	require.Equal(t, conn.LocalAddr().String(), srvConn.Connection().RemoteAddr().String())

	// Close the connection from client side.
	conn.Close()

	// Verify that the Server receives the closing notification.
	eventually(t, func() bool { return atomic.LoadInt32(&connectionCloseCalled) == 1 })
}

func TestDisconnectHttpConnection(t *testing.T) {
	// Verify Disconnect() results with Invalid HTTP Connection error
	err := httpConnection{}.Disconnect()
	assert.Error(t, err)
	assert.Equal(t, ErrInvalidHTTPConnection, err)
}

func TestDisconnectWSConnection(t *testing.T) {
	connectionCloseCalled := int32(0)
	callback := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectionCloseFunc: func(conn types.Connection) {
					atomic.StoreInt32(&connectionCloseCalled, 1)
				},
			}}
		},
	}

	// Start a Server.
	settings := &StartSettings{Settings: Settings{Callbacks: callback}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Connect to the Server.
	conn, _, err := dialClient(settings)

	// Verify that the connection is successful.
	assert.NoError(t, err)
	assert.True(t, atomic.LoadInt32(&connectionCloseCalled) == 0)

	// Close connection from server side
	srvConn := wsConnection{wsConn: conn}
	err = srvConn.Disconnect()
	assert.NoError(t, err)

	// Verify connection disconnected from server side
	eventually(t, func() bool { return atomic.LoadInt32(&connectionCloseCalled) == 1 })
	// Waiting for wsConnection to fail ReadMessage() over a Disconnected communication
	eventually(t, func() bool {
		_, _, err := conn.ReadMessage()
		return err != nil
	})
}

func TestServerReceiveSendMessage(t *testing.T) {
	var rcvMsg atomic.Value
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnMessageFunc: func(ctx context.Context, conn types.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
					// Remember received message.
					rcvMsg.Store(message)

					// Send a response.
					response := protobufs.ServerToAgent{
						InstanceUid:  message.InstanceUid,
						Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
					}
					return &response
				},
			}}
		},
	}

	// Start a Server.
	settings := &StartSettings{Settings: Settings{
		Callbacks:          callbacks,
		CustomCapabilities: []string{"local.test.capability"},
	}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Connect using a WebSocket client.
	conn, _, _ := dialClient(settings)
	require.NotNil(t, conn)
	defer conn.Close()

	// Send a message to the Server.
	sendMsg := protobufs.AgentToServer{
		InstanceUid: "12345678",
	}
	bytes, err := proto.Marshal(&sendMsg)
	require.NoError(t, err)
	err = conn.WriteMessage(websocket.BinaryMessage, bytes)
	require.NoError(t, err)

	// Wait until Server receives the message.
	eventually(t, func() bool { return rcvMsg.Load() != nil })
	assert.True(t, proto.Equal(rcvMsg.Load().(proto.Message), &sendMsg))

	// Read Server's response.
	mt, bytes, err := conn.ReadMessage()
	require.NoError(t, err)
	require.EqualValues(t, websocket.BinaryMessage, mt)

	// Decode the response.

	// Must start with a zero byte header until the end of grace period that ends Feb 1, 2023.
	require.EqualValues(t, 0, bytes[0])

	var response protobufs.ServerToAgent
	err = sharedinternal.DecodeWSMessage(bytes, &response)
	require.NoError(t, err)

	// Verify the response.
	assert.EqualValues(t, sendMsg.InstanceUid, response.InstanceUid)
	assert.EqualValues(t, protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus, response.Capabilities)
	assert.EqualValues(t, settings.CustomCapabilities, response.CustomCapabilities.Capabilities)
}

func TestServerReceiveSendMessageWithCompression(t *testing.T) {
	// Use highly compressible config body.
	uncompressedCfg := []byte(strings.Repeat("test", 10000))
	tests := []bool{false, true}
	for _, withCompression := range tests {
		t.Run(fmt.Sprintf("%v", withCompression), func(t *testing.T) {
			var rcvMsg atomic.Value
			callbacks := CallbacksStruct{
				OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
					return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
						OnMessageFunc: func(ctx context.Context, conn types.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
							// Remember received message.
							rcvMsg.Store(message)

							// Send a response.
							response := protobufs.ServerToAgent{
								InstanceUid:  message.InstanceUid,
								Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
								RemoteConfig: &protobufs.AgentRemoteConfig{
									Config: &protobufs.AgentConfigMap{
										ConfigMap: map[string]*protobufs.AgentConfigFile{
											"": {Body: uncompressedCfg},
										},
									},
								},
							}
							return &response
						},
					}}
				},
			}

			// Start a Server.
			settings := &StartSettings{Settings: Settings{Callbacks: callbacks, EnableCompression: withCompression}}
			srv := startServer(t, settings)
			defer srv.Stop(context.Background())

			// We use a transparent TCP proxy to be able to count the actual bytes transferred so that
			// we can test the number of actual bytes vs number of expected bytes with and without compression.
			proxy := testhelpers.NewProxy(settings.ListenEndpoint)
			assert.NoError(t, proxy.Start())

			serverSettings := *settings
			serverSettings.ListenEndpoint = proxy.IncomingEndpoint()
			// Connect using a WebSocket client.
			conn, _, _ := dialClient(&serverSettings)
			require.NotNil(t, conn)
			defer conn.Close()

			// Send a message to the Server.
			sendMsg := protobufs.AgentToServer{
				InstanceUid: "10000000",
				EffectiveConfig: &protobufs.EffectiveConfig{
					ConfigMap: &protobufs.AgentConfigMap{
						ConfigMap: map[string]*protobufs.AgentConfigFile{
							"": {Body: uncompressedCfg},
						},
					},
				},
			}
			bytes, err := proto.Marshal(&sendMsg)
			require.NoError(t, err)
			err = conn.WriteMessage(websocket.BinaryMessage, bytes)
			require.NoError(t, err)

			// Wait until Server receives the message.
			eventually(t, func() bool { return rcvMsg.Load() != nil })
			assert.True(t, proto.Equal(rcvMsg.Load().(proto.Message), &sendMsg))

			// Read Server's response.
			mt, bytes, err := conn.ReadMessage()
			require.NoError(t, err)
			require.EqualValues(t, websocket.BinaryMessage, mt)

			// Decode the response.

			// Must start with a zero byte header until the end of grace period that ends Feb 1, 2023.
			require.EqualValues(t, 0, bytes[0])

			var response protobufs.ServerToAgent
			err = sharedinternal.DecodeWSMessage(bytes, &response)
			require.NoError(t, err)

			fmt.Printf("sent %d, received %d\n", proxy.ClientToServerBytes(), proxy.ServerToClientBytes())

			// Verify the response.
			assert.EqualValues(t, sendMsg.InstanceUid, response.InstanceUid)
			assert.EqualValues(t, protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus, response.Capabilities)
			if withCompression {
				// With compression the entire bytes exchanged should be less than the config body.
				// This is only possible if there is any compression happening.
				assert.Less(t, proxy.ClientToServerBytes(), len(uncompressedCfg))
				assert.Less(t, proxy.ServerToClientBytes(), len(uncompressedCfg))
			} else {
				// Without compression the entire bytes exchanged should be more than the config body.
				assert.Greater(t, proxy.ClientToServerBytes(), len(uncompressedCfg))
				assert.Greater(t, proxy.ServerToClientBytes(), len(uncompressedCfg))
			}
		})
	}
}

func TestServerReceiveSendMessagePlainHTTP(t *testing.T) {
	var rcvMsg atomic.Value
	var onConnectedCalled, onCloseCalled int32
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectedFunc: func(ctx context.Context, conn types.Connection) {
					atomic.StoreInt32(&onConnectedCalled, 1)
				},
				OnMessageFunc: func(ctx context.Context, conn types.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
					// Remember received message.
					rcvMsg.Store(message)

					// Send a response.
					response := protobufs.ServerToAgent{
						InstanceUid:  message.InstanceUid,
						Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
					}
					return &response
				},
				OnConnectionCloseFunc: func(conn types.Connection) {
					atomic.StoreInt32(&onCloseCalled, 1)
				},
			}}
		},
	}

	// Start a Server.
	settings := &StartSettings{Settings: Settings{
		Callbacks:          callbacks,
		CustomCapabilities: []string{"local.test.capability"},
	}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Send a message to the Server.
	sendMsg := protobufs.AgentToServer{
		InstanceUid: "12345678",
	}
	b, err := proto.Marshal(&sendMsg)
	require.NoError(t, err)
	resp, err := http.Post("http://"+settings.ListenEndpoint+settings.ListenPath, contentTypeProtobuf, bytes.NewReader(b))
	require.NoError(t, err)

	// Wait until Server receives the message.
	eventually(t, func() bool { return rcvMsg.Load() != nil })
	assert.True(t, atomic.LoadInt32(&onConnectedCalled) == 1)

	// Verify the received message is what was sent.
	assert.True(t, proto.Equal(rcvMsg.Load().(proto.Message), &sendMsg))

	// Read Server's response.
	b, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.EqualValues(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, contentTypeProtobuf, resp.Header.Get(headerContentType))

	// Decode the response.
	var response protobufs.ServerToAgent
	err = proto.Unmarshal(b, &response)
	require.NoError(t, err)

	// Verify the response.
	assert.EqualValues(t, sendMsg.InstanceUid, response.InstanceUid)
	assert.EqualValues(t, protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus, response.Capabilities)
	assert.EqualValues(t, settings.CustomCapabilities, response.CustomCapabilities.Capabilities)

	eventually(t, func() bool { return atomic.LoadInt32(&onCloseCalled) == 1 })
}

func TestServerAttachAcceptConnection(t *testing.T) {
	connectedCalled := int32(0)
	connectionCloseCalled := int32(0)
	var srvConn types.Connection
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectedFunc: func(ctx context.Context, conn types.Connection) {
					atomic.StoreInt32(&connectedCalled, 1)
					srvConn = conn
				},
				OnConnectionCloseFunc: func(conn types.Connection) {
					atomic.StoreInt32(&connectionCloseCalled, 1)
					assert.EqualValues(t, srvConn, conn)
				},
			}}
		},
	}

	// Prepare to attach OpAMP Server to an HTTP Server created separately.
	settings := Settings{Callbacks: callbacks}
	srv := New(&sharedinternal.NopLogger{})
	require.NotNil(t, srv)
	handlerFunc, _, err := srv.Attach(settings)
	require.NoError(t, err)

	// Create an HTTP Server and make it handle OpAMP connections.
	mux := http.NewServeMux()
	path := "/opamppath"
	mux.HandleFunc(path, handlerFunc)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	// Connect using WebSocket client.
	srvUrl := "ws://" + hs.Listener.Addr().String() + path
	conn, resp, err := websocket.DefaultDialer.Dial(srvUrl, nil)

	// Verify that connection is successful.
	assert.NoError(t, err)
	assert.NotNil(t, conn)
	assert.EqualValues(t, 101, resp.StatusCode)
	eventually(t, func() bool { return atomic.LoadInt32(&connectedCalled) == 1 })
	assert.True(t, atomic.LoadInt32(&connectionCloseCalled) == 0)

	conn.Close()
	eventually(t, func() bool { return atomic.LoadInt32(&connectionCloseCalled) == 1 })
}

func TestServerAttachSendMessagePlainHTTP(t *testing.T) {
	connectedCalled := int32(0)
	connectionCloseCalled := int32(0)
	var rcvMsg atomic.Value

	var srvConn types.Connection
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectedFunc: func(ctx context.Context, conn types.Connection) {
					atomic.StoreInt32(&connectedCalled, 1)
					srvConn = conn
				},
				OnMessageFunc: func(ctx context.Context, conn types.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
					// Remember received message.
					rcvMsg.Store(message)

					// Send a response.
					response := protobufs.ServerToAgent{
						InstanceUid:  message.InstanceUid,
						Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
					}
					return &response
				},
				OnConnectionCloseFunc: func(conn types.Connection) {
					atomic.StoreInt32(&connectionCloseCalled, 1)
					assert.EqualValues(t, srvConn, conn)
				},
			}}
		},
	}

	// Prepare to attach OpAMP Server to an HTTP Server created separately.
	settings := Settings{Callbacks: callbacks}
	srv := New(&sharedinternal.NopLogger{})
	require.NotNil(t, srv)
	handlerFunc, ContextWithConn, err := srv.Attach(settings)
	require.NoError(t, err)

	// Create an HTTP Server and make it handle OpAMP connections.
	mux := http.NewServeMux()
	path := "/opamppath"
	mux.HandleFunc(path, handlerFunc)
	hs := httptest.NewUnstartedServer(mux)
	hs.Config.ConnContext = ContextWithConn
	hs.Start()
	defer hs.Close()

	// Send a message to the Server.
	sendMsg := protobufs.AgentToServer{
		InstanceUid: "12345678",
	}
	b, err := proto.Marshal(&sendMsg)
	require.NoError(t, err)
	resp, err := http.Post("http://"+hs.Listener.Addr().String()+path, contentTypeProtobuf, bytes.NewReader(b))
	require.NoError(t, err)

	// Wait until Server receives the message.
	eventually(t, func() bool { return rcvMsg.Load() != nil })
	assert.True(t, atomic.LoadInt32(&connectedCalled) == 1)

	// Verify the received message is what was sent.
	assert.True(t, proto.Equal(rcvMsg.Load().(proto.Message), &sendMsg))

	// Read Server's response.
	b, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.EqualValues(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, contentTypeProtobuf, resp.Header.Get(headerContentType))

	// Decode the response.
	var response protobufs.ServerToAgent
	err = proto.Unmarshal(b, &response)
	require.NoError(t, err)

	// Verify the response.
	assert.EqualValues(t, sendMsg.InstanceUid, response.InstanceUid)
	assert.EqualValues(t, protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus, response.Capabilities)

	eventually(t, func() bool { return atomic.LoadInt32(&connectionCloseCalled) == 1 })
}

func TestServerHonoursClientRequestContentEncoding(t *testing.T) {

	hc := http.Client{}
	var rcvMsg atomic.Value
	var onConnectedCalled, onCloseCalled int32
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectedFunc: func(ctx context.Context, conn types.Connection) {
					atomic.StoreInt32(&onConnectedCalled, 1)
				},
				OnMessageFunc: func(ctx context.Context, conn types.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
					// Remember received message.
					rcvMsg.Store(message)

					// Send a response.
					response := protobufs.ServerToAgent{
						InstanceUid:  message.InstanceUid,
						Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
					}
					return &response
				},
				OnConnectionCloseFunc: func(conn types.Connection) {
					atomic.StoreInt32(&onCloseCalled, 1)
				},
			}}
		},
	}

	// Start a Server.
	settings := &StartSettings{Settings: Settings{Callbacks: callbacks}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Send a message to the Server.
	sendMsg := protobufs.AgentToServer{
		InstanceUid: "12345678",
	}
	b, err := proto.Marshal(&sendMsg)
	require.NoError(t, err)

	// gzip compress the request payload
	b, err = compressGzip(b)
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "http://"+settings.ListenEndpoint+settings.ListenPath, bytes.NewReader(b))
	req.Header.Set(headerContentType, contentTypeProtobuf)
	req.Header.Set(headerContentEncoding, contentEncodingGzip)
	resp, err := hc.Do(req)
	require.NoError(t, err)

	// Wait until Server receives the message.
	eventually(t, func() bool { return rcvMsg.Load() != nil })
	assert.True(t, atomic.LoadInt32(&onConnectedCalled) == 1)

	// Verify the received message is what was sent.
	assert.True(t, proto.Equal(rcvMsg.Load().(proto.Message), &sendMsg))

	// Read Server's response.
	b, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.EqualValues(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, contentTypeProtobuf, resp.Header.Get(headerContentType))

	// Decode the response.
	var response protobufs.ServerToAgent
	err = proto.Unmarshal(b, &response)
	require.NoError(t, err)

	// Verify the response.
	assert.EqualValues(t, sendMsg.InstanceUid, response.InstanceUid)
	assert.EqualValues(t, protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus, response.Capabilities)

	eventually(t, func() bool { return atomic.LoadInt32(&onCloseCalled) == 1 })
}

func TestServerHonoursAcceptEncoding(t *testing.T) {

	hc := http.Client{}
	var rcvMsg atomic.Value
	var onConnectedCalled, onCloseCalled int32
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectedFunc: func(ctx context.Context, conn types.Connection) {
					atomic.StoreInt32(&onConnectedCalled, 1)
				},
				OnMessageFunc: func(ctx context.Context, conn types.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
					// Remember received message.
					rcvMsg.Store(message)

					// Send a response.
					response := protobufs.ServerToAgent{
						InstanceUid:  message.InstanceUid,
						Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
					}
					return &response
				},
				OnConnectionCloseFunc: func(conn types.Connection) {
					atomic.StoreInt32(&onCloseCalled, 1)
				},
			}}
		},
	}

	// Start a Server.
	settings := &StartSettings{Settings: Settings{Callbacks: callbacks}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Send a message to the Server.
	sendMsg := protobufs.AgentToServer{
		InstanceUid: "12345678",
	}
	b, err := proto.Marshal(&sendMsg)
	require.NoError(t, err)
	req, err := http.NewRequest("POST", "http://"+settings.ListenEndpoint+settings.ListenPath, bytes.NewReader(b))
	req.Header.Set(headerContentType, contentTypeProtobuf)
	req.Header.Set(headerAcceptEncoding, contentEncodingGzip)
	resp, err := hc.Do(req)
	require.NoError(t, err)

	// Wait until Server receives the message.
	eventually(t, func() bool { return rcvMsg.Load() != nil })
	assert.True(t, atomic.LoadInt32(&onConnectedCalled) == 1)

	// Verify the received message is what was sent.
	assert.True(t, proto.Equal(rcvMsg.Load().(proto.Message), &sendMsg))

	// Read Server's response.
	b, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	// Decompress the gzip response
	b, err = decompressGzip(b)
	require.NoError(t, err)

	assert.EqualValues(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, contentTypeProtobuf, resp.Header.Get(headerContentType))

	// Decode the response.
	var response protobufs.ServerToAgent
	err = proto.Unmarshal(b, &response)
	require.NoError(t, err)

	// Verify the response.
	assert.EqualValues(t, sendMsg.InstanceUid, response.InstanceUid)
	assert.EqualValues(t, protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus, response.Capabilities)

	eventually(t, func() bool { return atomic.LoadInt32(&onCloseCalled) == 1 })
}

func TestDecodeMessage(t *testing.T) {
	msgsToTest := []*protobufs.AgentToServer{
		{}, // Empty message
		{
			InstanceUid: "abcd",
			SequenceNum: 123,
		},
	}

	// Try with and without header byte. This is only necessary until the
	// end of grace period that ends Feb 1, 2023. After that the header is
	// no longer optional.
	withHeaderTests := []bool{false, true}

	for _, msg := range msgsToTest {
		for _, withHeader := range withHeaderTests {
			bytes, err := proto.Marshal(msg)
			require.NoError(t, err)

			if withHeader {
				// Prepend zero header byte.
				bytes = append([]byte{0}, bytes...)
			}

			var decoded protobufs.AgentToServer
			err = sharedinternal.DecodeWSMessage(bytes, &decoded)
			require.NoError(t, err)

			assert.True(t, proto.Equal(msg, &decoded))
		}
	}
}

func TestConnectionAllowsConcurrentWrites(t *testing.T) {
	srvConnVal := atomic.Value{}
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectedFunc: func(ctx context.Context, conn types.Connection) {
					srvConnVal.Store(conn)
				},
			}}
		},
	}

	// Start a Server.
	settings := &StartSettings{Settings: Settings{Callbacks: callbacks}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Connect to the Server.
	conn, _, err := dialClient(settings)

	// Verify that the connection is successful.
	assert.NoError(t, err)
	assert.NotNil(t, conn)

	defer conn.Close()

	timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	select {
	case <-timeout.Done():
		t.Error("Client failed to connect before timeout")
	default:
		if _, ok := srvConnVal.Load().(types.Connection); ok == true {
			break
		}
	}

	cancel()

	srvConn := srvConnVal.Load().(types.Connection)
	for i := 0; i < 20; i++ {
		go func() {
			defer func() {
				if recover() != nil {
					require.Fail(t, "Sending to client panicked")
				}
			}()

			srvConn.Send(context.Background(), &protobufs.ServerToAgent{})
		}()
	}
}

func BenchmarkSendToClient(b *testing.B) {
	clientConnections := []*websocket.Conn{}
	serverConnections := []types.Connection{}
	srvConnectionsMutex := sync.Mutex{}
	callbacks := CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true, ConnectionCallbacks: ConnectionCallbacksStruct{
				OnConnectedFunc: func(ctx context.Context, conn types.Connection) {
					srvConnectionsMutex.Lock()
					serverConnections = append(serverConnections, conn)
					srvConnectionsMutex.Unlock()
				},
			}}
		},
	}

	// Start a Server.
	settings := &StartSettings{
		Settings:       Settings{Callbacks: callbacks},
		ListenEndpoint: testhelpers.GetAvailableLocalAddress(),
		ListenPath:     "/",
	}
	srv := New(&sharedinternal.NopLogger{})
	err := srv.Start(*settings)

	if err != nil {
		b.Error(err)
	}

	defer srv.Stop(context.Background())

	for i := 0; i < b.N; i++ {
		conn, resp, err := dialClient(settings)

		if err != nil || resp == nil || conn == nil {
			b.Error("Could not establish connection:", err)
		}

		clientConnections = append(clientConnections, conn)
	}

	timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	select {
	case <-timeout.Done():
		b.Error("Connections failed to establish in time")
	default:
		if len(serverConnections) == b.N {
			break
		}
	}

	cancel()

	for _, conn := range serverConnections {
		err := conn.Send(context.Background(), &protobufs.ServerToAgent{})

		if err != nil {
			b.Error(err)
		}
	}

	for _, conn := range clientConnections {
		conn.Close()
	}

}
