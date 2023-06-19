package opampsrv

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"

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
	tlsConfig, err := createServerTLSConfig("../certs")
	if err != nil {
		srv.logger.Debugf("Could not load TLS config, working without TLS: %v", err.Error())
	}
	settings.TLSConfig = tlsConfig

	srv.opampSrv.Start(settings)
}

func createServerTLSConfig(certsDir string) (*tls.Config, error) {
	// Read the CA's public key. This is the CA that signs the server's certificate.
	caCertBytes, err := os.ReadFile(path.Join(certsDir, "certs/ca.cert.pem"))
	if err != nil {
		return nil, err
	}

	// Create a certificate pool and make our CA trusted.
	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(caCertBytes); !ok {
		return nil, errors.New("cannot append ca.cert.pem")
	}

	// Load server's certificate.
	cert, err := tls.LoadX509KeyPair(
		path.Join(certsDir, "server_certs/server.cert.pem"),
		path.Join(certsDir, "server_certs/server.key.pem"),
	)
	if err != nil {
		return nil, fmt.Errorf("tls.LoadX509KeyPair failed: %v", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// TODO: verify client cert manually, and allow TOFU option. See manual
		// verification example: https://dev.to/living_syn/validating-client-certificate-sans-in-go-i5p
		// Instead, we use VerifyClientCertIfGiven which will automatically verify the provided certificate
		// is signed by our CA (so TOFU with self-generated client certificate will not work).
		ClientAuth: tls.VerifyClientCertIfGiven,
		// Allow insecure connections for demo purposes.
		InsecureSkipVerify: true,
		ClientCAs:          caCertPool,
	}
	tlsConfig.BuildNameToCertificate()
	return tlsConfig, nil
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
