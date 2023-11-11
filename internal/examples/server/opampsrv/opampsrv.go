package opampsrv

import (
	"context"
	"log"
	"net/http"
	"os"
	"path"

	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	"github.com/open-telemetry/opamp-go/server/types"
)

type Server struct {
	opampSrv server.OpAMPServer
	agents   *data.Agents
	logger   *Logger
}

func NewServer(agents *data.Agents) *Server {
	logger := &Logger{
		log.New(
			log.Default().Writer(),
			"[OPAMP] ",
			log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds,
		),
	}

	srv := &Server{
		agents: agents,
		logger: logger,
	}

	srv.opampSrv = server.New(logger)

	return srv
}

func (srv *Server) Start() {
	settings := server.StartSettings{
		Settings: server.Settings{
			Callbacks: server.CallbacksStruct{
				OnConnectingFunc: func(request *http.Request) types.ConnectionResponse {
					return types.ConnectionResponse{
						Accept: true,
						ConnectionCallbacks: server.ConnectionCallbacksStruct{
							OnMessageFunc:         srv.onMessage,
							OnConnectionCloseFunc: srv.onDisconnect,
						},
					}
				},
			},
		},
		ListenEndpoint: "127.0.0.1:4320",
	}

	rootDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	certPath := path.Join(rootDir, "../certs/certs")
	serverCertPath := path.Join(rootDir, "../certs/server_certs")

	tlsConfig, err := internal.CreateServerTLSConfig(
		certPath+"/ca.cert.pem",
		serverCertPath+"/server.cert.pem",
		serverCertPath+"/server.key.pem",
	)
	if err != nil {
		srv.logger.Debugf("Could not load TLS config, working without TLS: %v", err.Error())
	}
	settings.TLSConfig = tlsConfig

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
