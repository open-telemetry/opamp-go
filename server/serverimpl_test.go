package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/protobufs"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/server/internal"
	"github.com/open-telemetry/opamp-go/server/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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

	// Wait until the server strts accepting connections.
	testhelpers.WaitForEndpoint(settings.ListenEndpoint)
	return srv
}

func dialClient(serverSettings *StartSettings) (*websocket.Conn, *http.Response, error) {
	srvUrl := "ws://" + serverSettings.ListenEndpoint + serverSettings.ListenPath
	return websocket.DefaultDialer.Dial(srvUrl, nil)
}

func TestServerStartStop(t *testing.T) {
	srv := startServer(t, &StartSettings{})

	err := srv.Start(StartSettings{})
	assert.ErrorIs(t, err, errAlreadyStarted)

	err = srv.Stop(context.Background())
	assert.NoError(t, err)
}

func TestServerStartRejectConnection(t *testing.T) {
	callbacks := internal.CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			// Reject the incoming HTTP connection.
			return types.ConnectionResponse{
				Accept:             false,
				HTTPStatusCode:     503,
				HTTPResponseHeader: map[string]string{"Retry-After": "30"},
			}
		},
	}

	// Start a server.
	settings := &StartSettings{Settings: Settings{Callbacks: callbacks}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Try to connect to the server.
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
	callbacks := internal.CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true}
		},
		OnConnectedFunc: func(conn types.Connection) {
			atomic.StoreInt32(&connectedCalled, 1)
			srvConn = conn
		},
		OnConnectionCloseFunc: func(conn types.Connection) {
			atomic.StoreInt32(&connectionCloseCalled, 1)
			assert.EqualValues(t, srvConn, conn)
		},
	}

	// Start a server.
	settings := &StartSettings{Settings: Settings{Callbacks: callbacks}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Connect to the server.
	conn, resp, err := dialClient(settings)

	// Verify that the connection is successful.
	assert.NoError(t, err)
	assert.NotNil(t, conn)
	assert.EqualValues(t, 101, resp.StatusCode)
	eventually(t, func() bool { return atomic.LoadInt32(&connectedCalled) == 1 })
	assert.True(t, atomic.LoadInt32(&connectionCloseCalled) == 0)

	// Close the connection from client side.
	conn.Close()

	// Verify that the server receives the closing notification.
	eventually(t, func() bool { return atomic.LoadInt32(&connectionCloseCalled) == 1 })
}

func TestServerReceiveSendMessage(t *testing.T) {
	var rcvMsg atomic.Value
	callbacks := internal.CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true}
		},
		OnMessageFunc: func(conn types.Connection, message *protobufs.AgentToServer) {
			// Remember received message.
			rcvMsg.Store(message)

			// Send a response.
			response := protobufs.ServerToAgent{
				InstanceUid:  message.InstanceUid,
				Capabilities: protobufs.ServerCapabilities_AcceptsStatus,
			}
			err := conn.Send(context.Background(), &response)
			assert.NoError(t, err)
		},
	}

	// Start a server.
	settings := &StartSettings{Settings: Settings{Callbacks: callbacks}}
	srv := startServer(t, settings)
	defer srv.Stop(context.Background())

	// Connect using a WebSocket client.
	conn, _, _ := dialClient(settings)
	defer conn.Close()

	// Send a message to the server.
	sendMsg := protobufs.AgentToServer{
		InstanceUid: "12345678",
	}
	bytes, err := proto.Marshal(&sendMsg)
	require.NoError(t, err)
	err = conn.WriteMessage(websocket.BinaryMessage, bytes)
	require.NoError(t, err)

	// Wait until server receives the message.
	eventually(t, func() bool { return rcvMsg.Load() != nil })
	assert.True(t, proto.Equal(rcvMsg.Load().(proto.Message), &sendMsg))

	// Read server's response.
	mt, bytes, err := conn.ReadMessage()
	require.NoError(t, err)
	require.EqualValues(t, websocket.BinaryMessage, mt)

	// Decode the response.
	var response protobufs.ServerToAgent
	err = proto.Unmarshal(bytes, &response)
	require.NoError(t, err)

	// Verify the response.
	assert.EqualValues(t, sendMsg.InstanceUid, response.InstanceUid)
	assert.EqualValues(t, protobufs.ServerCapabilities_AcceptsStatus, response.Capabilities)
}

func TestServerAttachAcceptConnection(t *testing.T) {
	connectedCalled := int32(0)
	connectionCloseCalled := int32(0)
	var srvConn types.Connection
	callbacks := internal.CallbacksStruct{
		OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
			return types.ConnectionResponse{Accept: true}
		},
		OnConnectedFunc: func(conn types.Connection) {
			atomic.StoreInt32(&connectedCalled, 1)
			srvConn = conn
		},
		OnConnectionCloseFunc: func(conn types.Connection) {
			atomic.StoreInt32(&connectionCloseCalled, 1)
			assert.EqualValues(t, srvConn, conn)
		},
	}

	// Prepare to attach OpAMP server to an HTTP Server created separately.
	settings := Settings{Callbacks: callbacks}
	srv := New(&sharedinternal.NopLogger{})
	require.NotNil(t, srv)
	handlerFunc, err := srv.Attach(settings)
	require.NoError(t, err)

	// Create an HTTP server and make it handle OpAMP connections.
	mux := http.NewServeMux()
	path := "/opamppath"
	mux.HandleFunc(path, handlerFunc)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	// Wait until server is ready and accepts connections.
	testhelpers.WaitForEndpoint(hs.Listener.Addr().String())

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
