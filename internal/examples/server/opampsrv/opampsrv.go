package opampsrv

import (
	"context"
	"log"

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
				OnMessageFunc:         srv.onMessage,
				OnConnectionCloseFunc: srv.onDisconnect,
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

	// Is there a status report?
	status := msg.GetStatusReport()
	if status != nil {
		// Process the status report and continue building the response.
		agent.UpdateStatus(status, response)
	}

	// Send the response back to the agent.
	return response
}
