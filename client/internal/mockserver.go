package internal

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
)

type MockServer struct {
	Endpoint    string
	OnRequest   func(w http.ResponseWriter, r *http.Request)
	OnConnect   func(r *http.Request)
	OnWSConnect func(conn *websocket.Conn)
	OnMessage   func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent
	srv         *httptest.Server
}

const headerContentType = "Content-Type"
const contentTypeProtobuf = "application/x-protobuf"

var upgrader = websocket.Upgrader{}

func StartMockServer(t *testing.T) *MockServer {
	srv := &MockServer{}

	m := http.NewServeMux()
	m.HandleFunc(
		"/", func(w http.ResponseWriter, r *http.Request) {
			if srv.OnRequest != nil {
				srv.OnRequest(w, r)
				return
			}

			if srv.OnConnect != nil {
				srv.OnConnect(r)
			}

			if r.Header.Get(headerContentType) == contentTypeProtobuf {
				srv.handlePlainHttp(w, r)
				return
			}

			srv.handleWebSocket(t, w, r)
		},
	)

	srv.srv = httptest.NewServer(m)

	u, err := url.Parse(srv.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv.Endpoint = u.Host

	testhelpers.WaitForEndpoint(srv.Endpoint)

	return srv
}

func (srv *MockServer) handlePlainHttp(w http.ResponseWriter, r *http.Request) {
	msgBytes, err := io.ReadAll(r.Body)

	var request protobufs.AgentToServer
	err = proto.Unmarshal(msgBytes, &request)
	if err != nil {
		log.Fatal("cannot decode:", err)
	}

	var response *protobufs.ServerToAgent
	if srv.OnMessage != nil {
		response = srv.OnMessage(&request)
	}
	if response == nil {
		response = &protobufs.ServerToAgent{
			InstanceUid: request.InstanceUid,
		}
	}
	msgBytes, err = proto.Marshal(response)
	if err != nil {
		log.Fatal("cannot encode:", err)
	}

	// Send the response.
	w.Header().Set(headerContentType, contentTypeProtobuf)
	_, err = w.Write(msgBytes)
	if err != nil {
		log.Fatal("cannot send:", err)
	}

}

func (srv *MockServer) handleWebSocket(t *testing.T, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if srv.OnWSConnect != nil {
		srv.OnWSConnect(conn)
	}
	for {
		var messageType int
		var msgBytes []byte
		if messageType, msgBytes, err = conn.ReadMessage(); err != nil {
			return
		}
		assert.EqualValues(t, websocket.BinaryMessage, messageType)

		var dest protobufs.AgentToServer
		err := proto.Unmarshal(msgBytes, &dest)
		if err != nil {
			log.Fatal("cannot decode:", err)
		}

		if srv.OnMessage != nil {
			response := srv.OnMessage(&dest)
			if response != nil {
				msgBytes, err := proto.Marshal(response)
				if err != nil {
					log.Fatal("cannot encode:", err)
				}
				err = conn.WriteMessage(websocket.BinaryMessage, msgBytes)
				if err != nil {
					log.Fatal("cannot send:", err)
				}
			}
		}
	}
}

func (m *MockServer) Close() {
	m.srv.Close()
}
