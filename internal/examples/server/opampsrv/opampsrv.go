package opampsrv

import (
	"context"
	"log"
	"net/http"

	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	"github.com/open-telemetry/opamp-go/server/types"
)

type Server struct {
	opampSrv server.OpAMPServer
	agents   *data.Agents
}

func NewServer(agents *data.Agents) *Server {
	srv := &Server{
		agents: agents,
	}

	logger := log.New(
		log.Default().Writer(),
		"[OPAMP] ",
		log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds,
	)

	srv.opampSrv = server.New(&Logger{logger})

	return srv
}

func (srv *Server) Start() {
	settings := server.StartSettings{
		Settings: server.Settings{
			Callbacks: server.CallbacksStruct{
				OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
					return types.ConnectionResponse{Accept: true, ConnectionHandler: server.ConnectionCallbacksStruct{
						OnMessageFunc:         srv.onMessage,
						OnConnectionCloseFunc: srv.onDisconnect,
					}}
				},
			},
		},
		ListenEndpoint: "127.0.0.1:4320",
	}

	srv.opampSrv.Start(settings)
}

func (srv *Server) Stop() {
	srv.opampSrv.Stop(context.Background())
}

func (srv *Server) onDisconnect(conn types.Connection) {
	srv.agents.RemoveConnection(conn)
}

func (srv *Server) onMessage(conn types.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	instanceId := data.InstanceId(msg.InstanceUid)

	agent := srv.agents.FindOrCreateAgent(instanceId, conn)

	// Start building the response.
	response := &protobufs.ServerToAgent{}

	// Process the status report and continue building the response.
	agent.UpdateStatus(msg, response)

	// Send the response back to the Agent.
	return response
}
