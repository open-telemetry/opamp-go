package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/oklog/ulid/v2"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

const localConfig = `
exporters:
  otlp:
    endpoint: localhost:1111

receivers:
  otlp:
    protocols:
      grpc: {}
      http: {}

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: []
      exporters: [otlp]
`

type Agent struct {
	logger types.Logger

	agentType    string
	agentVersion string

	effectiveConfig string

	instanceId ulid.ULID

	agentDescription *protobufs.AgentDescription

	opampClient client.OpAMPClient

	remoteConfigStatus *protobufs.RemoteConfigStatus

	metricReporter *MetricReporter

	// The TLS certificate used for the OpAMP connection. Can be nil, meaning no client-side
	// certificate is used.
	opampClientCert *tls.Certificate
}

func NewAgent(logger types.Logger, agentType string, agentVersion string) *Agent {
	agent := &Agent{
		effectiveConfig: localConfig,
		logger:          logger,
		agentType:       agentType,
		agentVersion:    agentVersion,
	}

	agent.createAgentIdentity()
	agent.logger.Debugf("Agent starting, id=%v, type=%s, version=%s.",
		agent.instanceId.String(), agentType, agentVersion)

	agent.loadLocalConfig()
	if err := agent.connect(); err != nil {
		agent.logger.Errorf("Cannot connect OpAMP client: %v", err)
		return nil
	}

	return agent
}

func (agent *Agent) connect() error {
	agent.opampClient = client.NewWebSocket(agent.logger)

	tlsConfig, err := internal.CreateClientTLSConfig(agent.opampClientCert, "../../certs")
	if err != nil {
		return err
	}

	settings := types.StartSettings{
		OpAMPServerURL: "wss://127.0.0.1:4320/v1/opamp",
		TLSConfig:      tlsConfig,
		InstanceUid:    agent.instanceId.String(),
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func() {
				agent.logger.Debugf("Connected to the server.")
			},
			OnConnectFailedFunc: func(err error) {
				agent.logger.Errorf("Failed to connect to the server: %v", err)
			},
			OnErrorFunc: func(err *protobufs.ServerErrorResponse) {
				agent.logger.Errorf("Server returned an error response: %v", err.ErrorMessage)
			},
			SaveRemoteConfigStatusFunc: func(_ context.Context, status *protobufs.RemoteConfigStatus) {
				agent.remoteConfigStatus = status
			},
			GetEffectiveConfigFunc: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
				return agent.composeEffectiveConfig(), nil
			},
			OnMessageFunc:                 agent.onMessage,
			OnOpampConnectionSettingsFunc: agent.onOpampConnectionSettings,
		},
		RemoteConfigStatus: agent.remoteConfigStatus,
		Capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics |
			protobufs.AgentCapabilities_AgentCapabilities_AcceptsOpAMPConnectionSettings,
	}

	err = agent.opampClient.SetAgentDescription(agent.agentDescription)
	if err != nil {
		return err
	}

	agent.logger.Debugf("Starting OpAMP client...")

	err = agent.opampClient.Start(context.Background(), settings)
	if err != nil {
		return err
	}

	agent.logger.Debugf("OpAMP Client started.")

	return nil
}

func (agent *Agent) disconnect() {
	agent.logger.Debugf("Disconnecting from server...")
	agent.opampClient.Stop(context.Background())
}

func (agent *Agent) createAgentIdentity() {
	// Generate instance id.
	entropy := ulid.Monotonic(rand.New(rand.NewSource(0)), 0)
	agent.instanceId = ulid.MustNew(ulid.Timestamp(time.Now()), entropy)

	hostname, _ := os.Hostname()

	// Create Agent description.
	agent.agentDescription = &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: agent.agentType},
				},
			},
			{
				Key: "service.version",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: agent.agentVersion},
				},
			},
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key: "os.type",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{
						StringValue: runtime.GOOS,
					},
				},
			},
			{
				Key: "host.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{
						StringValue: hostname,
					},
				},
			},
		},
	}
}

func (agent *Agent) updateAgentIdentity(instanceId ulid.ULID) {
	agent.logger.Debugf("Agent identify is being changed from id=%v to id=%v",
		agent.instanceId.String(),
		instanceId.String())
	agent.instanceId = instanceId

	if agent.metricReporter != nil {
		// TODO: reinit or update meter (possibly using a single function to update all own connection settings
		// or with having a common resource factory or so)
	}
}

func (agent *Agent) loadLocalConfig() {
	var k = koanf.New(".")
	_ = k.Load(rawbytes.Provider([]byte(localConfig)), yaml.Parser())

	effectiveConfigBytes, err := k.Marshal(yaml.Parser())
	if err != nil {
		panic(err)
	}

	agent.effectiveConfig = string(effectiveConfigBytes)
}

func (agent *Agent) composeEffectiveConfig() *protobufs.EffectiveConfig {
	return &protobufs.EffectiveConfig{
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"": {Body: []byte(agent.effectiveConfig)},
			},
		},
	}
}

func (agent *Agent) initMeter(settings *protobufs.TelemetryConnectionSettings) {
	reporter, err := NewMetricReporter(agent.logger, settings, agent.agentType, agent.agentVersion, agent.instanceId)
	if err != nil {
		agent.logger.Errorf("Cannot collect metrics: %v", err)
		return
	}

	prevReporter := agent.metricReporter

	agent.metricReporter = reporter

	if prevReporter != nil {
		prevReporter.Shutdown()
	}

	return
}

type agentConfigFileItem struct {
	name string
	file *protobufs.AgentConfigFile
}

type agentConfigFileSlice []agentConfigFileItem

func (a agentConfigFileSlice) Less(i, j int) bool {
	return a[i].name < a[j].name
}

func (a agentConfigFileSlice) Swap(i, j int) {
	t := a[i]
	a[i] = a[j]
	a[j] = t
}

func (a agentConfigFileSlice) Len() int {
	return len(a)
}

func (agent *Agent) applyRemoteConfig(config *protobufs.AgentRemoteConfig) (configChanged bool, err error) {
	if config == nil {
		return false, nil
	}

	agent.logger.Debugf("Received remote config from server, hash=%x.", config.ConfigHash)

	// Begin with local config. We will later merge received configs on top of it.
	var k = koanf.New(".")
	if err := k.Load(rawbytes.Provider([]byte(localConfig)), yaml.Parser()); err != nil {
		return false, err
	}

	orderedConfigs := agentConfigFileSlice{}
	for name, file := range config.Config.ConfigMap {
		if name == "" {
			// skip instance config
			continue
		}
		orderedConfigs = append(orderedConfigs, agentConfigFileItem{
			name: name,
			file: file,
		})
	}

	// Sort to make sure the order of merging is stable.
	sort.Sort(orderedConfigs)

	// Append instance config as the last item.
	instanceConfig := config.Config.ConfigMap[""]
	if instanceConfig != nil {
		orderedConfigs = append(orderedConfigs, agentConfigFileItem{
			name: "",
			file: instanceConfig,
		})
	}

	// Merge received configs.
	for _, item := range orderedConfigs {
		var k2 = koanf.New(".")
		err := k2.Load(rawbytes.Provider(item.file.Body), yaml.Parser())
		if err != nil {
			return false, fmt.Errorf("cannot parse config named %s: %v", item.name, err)
		}
		err = k.Merge(k2)
		if err != nil {
			return false, fmt.Errorf("cannot merge config named %s: %v", item.name, err)
		}
	}

	// The merged final result is our effective config.
	effectiveConfigBytes, err := k.Marshal(yaml.Parser())
	if err != nil {
		panic(err)
	}

	newEffectiveConfig := string(effectiveConfigBytes)
	configChanged = false
	if agent.effectiveConfig != newEffectiveConfig {
		agent.logger.Debugf("Effective config changed. Need to report to server.")
		agent.effectiveConfig = newEffectiveConfig
		configChanged = true
	}

	return configChanged, nil
}

func (agent *Agent) Shutdown() {
	agent.logger.Debugf("Agent shutting down...")
	if agent.opampClient != nil {
		_ = agent.opampClient.Stop(context.Background())
	}
}

func (agent *Agent) onMessage(ctx context.Context, msg *types.MessageData) {
	configChanged := false
	if msg.RemoteConfig != nil {
		var err error
		configChanged, err = agent.applyRemoteConfig(msg.RemoteConfig)
		if err != nil {
			agent.opampClient.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
				LastRemoteConfigHash: msg.RemoteConfig.ConfigHash,
				Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
				ErrorMessage:         err.Error(),
			})
		} else {
			agent.opampClient.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
				LastRemoteConfigHash: msg.RemoteConfig.ConfigHash,
				Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
			})
		}
	}

	if msg.OwnMetricsConnSettings != nil {
		agent.initMeter(msg.OwnMetricsConnSettings)
	}

	if msg.AgentIdentification != nil {
		newInstanceId, err := ulid.Parse(msg.AgentIdentification.NewInstanceUid)
		if err != nil {
			agent.logger.Errorf(err.Error())
		}
		agent.updateAgentIdentity(newInstanceId)
	}

	if configChanged {
		err := agent.opampClient.UpdateEffectiveConfig(ctx)
		if err != nil {
			agent.logger.Errorf(err.Error())
		}
	}
}

func (agent *Agent) tryChangeOpAMPCert(cert *tls.Certificate) {
	agent.logger.Debugf("Reconnecting to verify offered client certificate.\n")

	agent.disconnect()

	agent.opampClientCert = cert
	if err := agent.connect(); err != nil {
		agent.logger.Errorf("Cannot connect using offered certificate: %s. Ignoring the offer\n", err)
		agent.opampClientCert = nil

		if err := agent.connect(); err != nil {
			agent.logger.Errorf("Unable to reconnect after restoring client certificate: %v\n", err)
		}
	}

	agent.logger.Debugf("Successfully connected to server. Accepting new client certificate.\n")

	// TODO: we can also persist the successfully accepted certificate and use it when the
	// agent connects to the server after the restart.
}

func (agent *Agent) onOpampConnectionSettings(ctx context.Context, settings *protobufs.OpAMPConnectionSettings) error {
	if settings == nil || settings.Certificate == nil {
		agent.logger.Debugf("Received nil certificate offer, ignoring.\n")
		return nil
	}

	cert, err := agent.getCertFromSettings(settings.Certificate)
	if err != nil {
		return err
	}

	// TODO: also use settings.DestinationEndpoint and settings.Headers for future connections.
	go agent.tryChangeOpAMPCert(cert)

	return nil
}

func (agent *Agent) getCertFromSettings(certificate *protobufs.TLSCertificate) (*tls.Certificate, error) {
	// Parse the key pair to a certificate that can be used for network connections.
	cert, err := tls.X509KeyPair(
		certificate.PublicKey,
		certificate.PrivateKey,
	)
	if err != nil {
		agent.logger.Errorf("Received invalid certificate offer: %s\n", err)
		return nil, err
	}

	if len(certificate.CaPublicKey) != 0 {
		caCertPB, _ := pem.Decode(certificate.CaPublicKey)
		caCert, err := x509.ParseCertificate(caCertPB.Bytes)
		if err != nil {
			agent.logger.Errorf("Cannot parse CA cert: %v", err)
			return nil, err
		}
		agent.logger.Debugf("Received offer signed by CA: %v", caCert.Subject)
		// TODO: we can verify the CA's identity here (to match our CA as we know it).
	}

	return &cert, nil
}
