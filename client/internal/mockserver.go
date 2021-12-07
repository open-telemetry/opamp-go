package internal

import (
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
	Endpoint  string
	OnRequest func(w http.ResponseWriter, r *http.Request)
	OnConnect func(r *http.Request, conn *websocket.Conn)
	OnMessage func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent
	srv       *httptest.Server
}

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

			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			if srv.OnConnect != nil {
				srv.OnConnect(r, conn)
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

func (m *MockServer) Close() {
	m.srv.Close()
}
