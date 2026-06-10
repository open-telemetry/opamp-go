//go:build cwebsocket

package internal

import (
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"

	"github.com/open-telemetry/opamp-go/protobufs"
)

// MockServer is the test mock server for WebSocket connections.
type MockServer struct {
	t           *testing.T
	Endpoint    string
	OnRequest   func(w http.ResponseWriter, r *http.Request)
	OnConnect   func(r *http.Request)
	OnWSConnect func(conn *websocket.Conn)
	OnMessage   func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent
	srv         *httptest.Server

	expectedHandlers  chan receivedMessageHandler
	expectedComplete  chan struct{}
	isExpectMode      bool
	enableCompression bool
}

func (m *MockServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	var acceptOpts *websocket.AcceptOptions
	if m.enableCompression {
		acceptOpts = &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover}
	}
	conn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		return
	}
	if m.OnWSConnect != nil {
		m.OnWSConnect(conn)
	}
	for {
		var messageType websocket.MessageType
		var msgBytes []byte
		if messageType, msgBytes, err = conn.Read(r.Context()); err != nil {
			return
		}
		assert.EqualValues(m.t, websocket.MessageBinary, messageType)

		if len(msgBytes) > 0 && msgBytes[0] == 0 {
			// New message format. The Protobuf message is preceded by a zero byte header.
			// Skip the zero byte.
			msgBytes = msgBytes[1:]
		}

		// We use alwaysRespond=false here because WebSocket requests must only have
		// a response when a response is provided by the user-defined handler.
		msgBytes = m.handleReceivedBytes(msgBytes, false)
		if msgBytes != nil {
			// Prepend zero-byte header.
			msgBytes = append([]byte{0}, msgBytes...)

			err = conn.Write(r.Context(), websocket.MessageBinary, msgBytes)
			if err != nil {
				log.Fatal("cannot send:", err)
			}
		}
	}
}
