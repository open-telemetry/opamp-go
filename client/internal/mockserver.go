package internal

import (
	"log"
	"net/http"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/internal/protobufs"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
)

type MockServer struct {
	Endpoint  string
	OnRequest func(w http.ResponseWriter, r *http.Request)
	OnConnect func(r *http.Request, conn *websocket.Conn)
	OnMessage func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent
	HTTPSrv   *http.Server
}

var upgrader = websocket.Upgrader{}

func StartMockServer(t *testing.T) *MockServer {
	srv := &MockServer{Endpoint: testhelpers.GetAvailableLocalAddress()}

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
	srv.HTTPSrv = &http.Server{
		Handler: m,
		Addr:    srv.Endpoint,
	}
	go srv.HTTPSrv.ListenAndServe()

	testhelpers.WaitForEndpoint(srv.Endpoint)

	return srv
}
