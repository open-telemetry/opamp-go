package opampsrv

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/oklog/ulid/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/open-telemetry/opamp-go/internal/examples/certs"
	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	"github.com/open-telemetry/opamp-go/server/types"
)

type Server struct {
	opampSrv server.OpAMPServer
	agents   *data.Agents
	logger   *Logger
	metrics  *metricsTracker
}

func NewServer(agents *data.Agents, emitMetrics bool) *Server {
	logger := &Logger{
		log.New(
			log.Default().Writer(),
			"[OPAMP] ",
			log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds,
		),
	}

	metrics, err := NewMetricsTracker(emitMetrics)
	if err != nil {
		panic(err)
	}

	srv := &Server{
		agents:  agents,
		logger:  logger,
		metrics: metrics,
	}

	srv.opampSrv = server.New(logger)

	return srv
}

func (srv *Server) Start() {
	settings := server.StartSettings{
		Settings: server.Settings{
			Callbacks: types.Callbacks{
				OnConnecting: func(request *http.Request) types.ConnectionResponse {
					return types.ConnectionResponse{
						Accept: true,
						ConnectionCallbacks: types.ConnectionCallbacks{
							OnConnected:       func(ctx context.Context, _ types.Connection) { srv.metrics.OnConnected(ctx) },
							OnMessage:         srv.onMessage,
							OnConnectionClose: srv.onDisconnect,
							OnReadMessageError: func(_ types.Connection, _ int, _ []byte, _ error) {
								srv.metrics.OnReadMessageError(context.Background())
							},
							OnMessageResponseError: func(_ types.Connection, _ *protobufs.ServerToAgent, _ error) {
								srv.metrics.OnMessageResponseError(context.Background())
							},
						},
					}
				},
			},
		},
		ListenEndpoint: "0.0.0.0:4320",
		HTTPMiddleware: otelhttp.NewMiddleware("/v1/opamp"),
	}
	tlsConfig, err := certs.CreateServerTLSConfig(
		certs.CaCert,
		certs.ServerCert,
		certs.ServerKey,
	)
	if err != nil {
		srv.logger.Debugf(context.Background(), "Could not load TLS config, working without TLS: %v", err.Error())
	}
	settings.TLSConfig = tlsConfig

	if err := srv.opampSrv.Start(settings); err != nil {
		srv.logger.Errorf(context.Background(), "OpAMP server start fail: %v", err.Error())
		os.Exit(1)
	}
}

func (srv *Server) Stop() {
	srv.opampSrv.Stop(context.Background())
}

func (srv *Server) onDisconnect(conn types.Connection) {
	srv.metrics.OnDisconnect(context.Background())
	srv.agents.RemoveConnection(conn)
}

func (srv *Server) onMessage(ctx context.Context, conn types.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	// Start building the response.
	response := &protobufs.ServerToAgent{}

	var instanceId data.InstanceId
	if len(msg.InstanceUid) == 26 {
		// This is an old-style ULID.
		u, err := ulid.Parse(string(msg.InstanceUid))
		if err != nil {
			srv.logger.Errorf(ctx, "Cannot parse ULID %s: %v", string(msg.InstanceUid), err)
			return response
		}
		instanceId = data.InstanceId(u.Bytes())
	} else if len(msg.InstanceUid) == 16 {
		// This is a 16 byte, new style UID.
		instanceId = data.InstanceId(msg.InstanceUid)
	} else {
		srv.logger.Errorf(ctx, "Invalid length of msg.InstanceUid")
		return response
	}

	srv.logger.Debugf(ctx, "AgentToServer: InstanceUid=%x, %s", instanceId, formatAgentToServer(msg))

	agent := srv.agents.FindOrCreateAgent(instanceId, conn)

	// Process the status report and continue building the response.
	agent.UpdateStatus(msg, response)

	// Send the response back to the Agent.
	srv.logger.Debugf(ctx, "ServerToAgent: InstanceUid=%x, %s", instanceId, formatServerToAgent(response))
	return response
}

func formatAgentToServer(msg *protobufs.AgentToServer) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("SequenceNum=%d", msg.SequenceNum))
	if msg.AgentDescription != nil {
		parts = append(parts, fmt.Sprintf("AgentDescription=%s", msg.AgentDescription))
	}
	parts = append(parts, fmt.Sprintf("Capabilities=[%s]", formatBitmask(msg.Capabilities, protobufs.AgentCapabilities_name, "AgentCapabilities_")))
	if msg.Health != nil {
		parts = append(parts, fmt.Sprintf("Healthy=%v", msg.Health.Healthy))
	}
	if msg.EffectiveConfig != nil && msg.EffectiveConfig.ConfigMap != nil {
		parts = append(parts, fmt.Sprintf("EffectiveConfigFiles=%d", len(msg.EffectiveConfig.ConfigMap.ConfigMap)))
	}
	if msg.RemoteConfigStatus != nil {
		parts = append(parts, fmt.Sprintf("RemoteConfigStatus=%s", strings.TrimPrefix(msg.RemoteConfigStatus.Status.String(), "RemoteConfigStatuses_")))
		if len(msg.RemoteConfigStatus.LastRemoteConfigHash) > 0 {
			parts = append(parts, fmt.Sprintf("RemoteConfigStatusHash=%x", msg.RemoteConfigStatus.LastRemoteConfigHash))
		}
		if msg.RemoteConfigStatus.ErrorMessage != "" {
			parts = append(parts, fmt.Sprintf("RemoteConfigStatusErrorMessage=%q", msg.RemoteConfigStatus.ErrorMessage))
		}
	}
	if msg.PackageStatuses != nil {
		if len(msg.PackageStatuses.ServerProvidedAllPackagesHash) > 0 {
			parts = append(parts, fmt.Sprintf("ServerProvidedAllPackagesHash=%x", msg.PackageStatuses.ServerProvidedAllPackagesHash))
		}
		if msg.PackageStatuses.ErrorMessage != "" {
			parts = append(parts, fmt.Sprintf("PackageStatusesErrorMessage=%q", msg.PackageStatuses.ErrorMessage))
		}
	}
	if msg.AgentDisconnect != nil {
		parts = append(parts, "AgentDisconnect=<set>")
	}
	if msg.Flags != 0 {
		parts = append(parts, fmt.Sprintf("Flags=[%s]", formatBitmask(msg.Flags, protobufs.AgentToServerFlags_name, "AgentToServerFlags_")))
	}
	if msg.ConnectionSettingsRequest != nil && msg.ConnectionSettingsRequest.Opamp != nil {
		parts = append(parts, "ConnectionSettingsRequest=<set>")
	}
	if msg.CustomCapabilities != nil {
		parts = append(parts, fmt.Sprintf("CustomCapabilities=%v", msg.CustomCapabilities.Capabilities))
	}
	if msg.CustomMessage != nil {
		parts = append(parts, fmt.Sprintf("CustomMessage={Capability=%q, Type=%q}", msg.CustomMessage.GetCapability(), msg.CustomMessage.GetType()))
	}
	if msg.AvailableComponents != nil {
		if len(msg.AvailableComponents.Hash) > 0 {
			parts = append(parts, fmt.Sprintf("AvailableComponentsHash=%x", msg.AvailableComponents.Hash))
		}
	}
	if msg.ConnectionSettingsStatus != nil {
		parts = append(parts, fmt.Sprintf("ConnectionSettingsStatus=%s", strings.TrimPrefix(msg.ConnectionSettingsStatus.Status.String(), "ConnectionSettingsStatuses_")))
		if len(msg.ConnectionSettingsStatus.LastConnectionSettingsHash) > 0 {
			parts = append(parts, fmt.Sprintf("ConnectionSettingsStatusHash=%x", msg.ConnectionSettingsStatus.LastConnectionSettingsHash))
		}
		if msg.ConnectionSettingsStatus.ErrorMessage != "" {
			parts = append(parts, fmt.Sprintf("ConnectionSettingsStatusErrorMessage=%q", msg.ConnectionSettingsStatus.ErrorMessage))
		}
	}
	return strings.Join(parts, ", ")
}

func formatServerToAgent(msg *protobufs.ServerToAgent) string {
	var parts []string
	if msg.ErrorResponse != nil {
		parts = append(parts, fmt.Sprintf("ErrorResponse=%s", msg.ErrorResponse))
	}
	if msg.RemoteConfig != nil && len(msg.RemoteConfig.ConfigHash) > 0 {
		parts = append(parts, fmt.Sprintf("RemoteConfigHash=%x", msg.RemoteConfig.ConfigHash))
	}
	if msg.ConnectionSettings != nil && len(msg.ConnectionSettings.Hash) > 0 {
		parts = append(parts, fmt.Sprintf("ConnectionSettingsHash=%x", msg.ConnectionSettings.Hash))
	}
	if msg.PackagesAvailable != nil && len(msg.PackagesAvailable.AllPackagesHash) > 0 {
		parts = append(parts, fmt.Sprintf("AllPackagesHash=%x", msg.PackagesAvailable.AllPackagesHash))
	}
	if msg.Flags != 0 {
		parts = append(parts, fmt.Sprintf("Flags=[%s]", formatBitmask(msg.Flags, protobufs.ServerToAgentFlags_name, "ServerToAgentFlags_")))
	}
	if msg.Capabilities != 0 {
		parts = append(parts, fmt.Sprintf("Capabilities=[%s]", formatBitmask(msg.Capabilities, protobufs.ServerCapabilities_name, "ServerCapabilities_")))
	}
	if msg.AgentIdentification != nil {
		parts = append(parts, fmt.Sprintf("AgentIdentification=%s", msg.AgentIdentification))
	}
	if msg.Command != nil {
		parts = append(parts, fmt.Sprintf("Command=%s", msg.Command))
	}
	if msg.CustomCapabilities != nil {
		parts = append(parts, fmt.Sprintf("CustomCapabilities=%v", msg.CustomCapabilities.Capabilities))
	}
	if msg.CustomMessage != nil {
		parts = append(parts, fmt.Sprintf("CustomMessage={Capability=%q, Type=%q}", msg.CustomMessage.GetCapability(), msg.CustomMessage.GetType()))
	}
	return strings.Join(parts, ", ")
}

func formatBitmask(value uint64, nameMap map[int32]string, prefix string) string {
	if value == 0 {
		return strings.TrimPrefix(nameMap[0], prefix)
	}
	var names []string
	remaining := value
	for bit := uint64(1); remaining != 0; bit <<= 1 {
		if value&bit != 0 {
			if name, ok := nameMap[int32(bit)]; ok {
				names = append(names, strings.TrimPrefix(name, prefix))
			} else {
				names = append(names, fmt.Sprintf("Unknown(0x%x)", bit))
			}
			remaining &^= bit
		}
	}
	return strings.Join(names, "|")
}
