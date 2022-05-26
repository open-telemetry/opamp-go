package supervisor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/oklog/ulid/v2"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/examples/supervisor/supervisor/commander"
	"github.com/open-telemetry/opamp-go/internal/examples/supervisor/supervisor/config"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// This Supervisor is developed specifically for OpenTelemetry Collector.
const agentType = "io.opentelemetry.collector"

// TODO: fetch agent version from Collector executable or by some other means.
const agentVersion = "1.0.0"

// A Collector config that should be always applied.
// Enables JSON log output for the Agent.
const localOverrideAgentConfig = `
service:
  telemetry:
    logs:
      encoding: json
`

// Supervisor implements supervising of OpenTelemetry Collector and uses OpAMPClient
// to work with an OpAMP Server.
type Supervisor struct {
	logger types.Logger

	// Commander that starts/stops the Agent process.
	commander *commander.Commander

	// Supervisor's own config.
	config config.Supervisor

	// The version of the Agent being Supervised.
	agentVersion string

	// Agent's instance id.
	instanceId ulid.ULID

	// A config section to be added to the Collector's config to fetch its own metrics.
	// TODO: store this persistently so that when starting we can compose the effective
	// config correctly.
	agentConfigOwnMetricsSection atomic.Value

	// Final effective config of the Collector.
	effectiveConfig atomic.Value

	// Location of the effective config file.
	effectiveConfigFilePath string

	// Last received remote config.
	remoteConfig *protobufs.AgentRemoteConfig

	// A channel to indicate there is a new config to apply.
	hasNewConfig chan struct{}

	// The OpAMP client to connect to the OpAMP Server.
	opampClient client.OpAMPClient
}

func NewSupervisor(logger types.Logger) (*Supervisor, error) {
	s := &Supervisor{
		logger:                  logger,
		agentVersion:            agentVersion,
		hasNewConfig:            make(chan struct{}, 1),
		effectiveConfigFilePath: "effective.yaml",
	}

	if err := s.loadConfig(); err != nil {
		return nil, fmt.Errorf("Error loading config: %v", err)
	}

	s.createInstanceId()
	logger.Debugf("Supervisor starting, id=%v, type=%s, version=%s.",
		s.instanceId.String(), agentType, agentVersion)

	s.loadAgentEffectiveConfig()

	if err := s.startOpAMP(); err != nil {
		return nil, fmt.Errorf("Cannot startOpAMP OpAMP client: %v", err)
	}

	var err error
	s.commander, err = commander.NewCommander(
		s.logger,
		s.config.Agent,
		"--config", s.effectiveConfigFilePath,
	)
	if err != nil {
		return nil, err
	}

	go s.runAgentProcess()

	return s, nil
}

func (s *Supervisor) loadConfig() error {
	const configFile = "supervisor.yaml"

	k := koanf.New("::")
	if err := k.Load(file.Provider(configFile), yaml.Parser()); err != nil {
		return err
	}

	if err := k.Unmarshal("", &s.config); err != nil {
		return fmt.Errorf("cannot parse %v: %w", configFile, err)
	}

	return nil
}

func (s *Supervisor) startOpAMP() error {

	s.opampClient = client.NewWebSocket(s.logger)

	settings := types.StartSettings{
		OpAMPServerURL: s.config.Server.Endpoint,
		InstanceUid:    s.instanceId.String(),
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func() {
				s.logger.Debugf("Connected to the server.")
			},
			OnConnectFailedFunc: func(err error) {
				s.logger.Errorf("Failed to connect to the server: %v", err)
			},
			OnErrorFunc: func(err *protobufs.ServerErrorResponse) {
				s.logger.Errorf("Server returned an error response: %v", err.ErrorMessage)
			},
			OnRemoteConfigFunc:                   s.onRemoteConfig,
			OnOwnTelemetryConnectionSettingsFunc: s.onOwnTelemetryConnectionSettings,
			OnAgentIdentificationFunc:            s.onAgentIdentificationFunc,
			GetEffectiveConfigFunc: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
				return s.createEffectiveConfigMsg(), nil
			},
		},
	}
	err := s.opampClient.SetAgentDescription(s.createAgentDescription())
	if err != nil {
		return err
	}

	s.logger.Debugf("Starting OpAMP client...")

	err = s.opampClient.Start(context.Background(), settings)
	if err != nil {
		return err
	}

	s.logger.Debugf("OpAMP Client started.")

	return nil
}

func (s *Supervisor) createInstanceId() {
	// Generate instance id.
	entropy := ulid.Monotonic(rand.New(rand.NewSource(0)), 0)
	s.instanceId = ulid.MustNew(ulid.Timestamp(time.Now()), entropy)

	// TODO: set instanceId in the Collector config.
}

func keyVal(key, val string) *protobufs.KeyValue {
	return &protobufs.KeyValue{
		Key: key,
		Value: &protobufs.AnyValue{
			Value: &protobufs.AnyValue_StringValue{StringValue: val},
		},
	}
}

func (s *Supervisor) createAgentDescription() *protobufs.AgentDescription {
	hostname, _ := os.Hostname()

	// Create Agent description.
	return &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			keyVal("service.name", agentType),
			keyVal("service.version", s.agentVersion),
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			keyVal("os.family", runtime.GOOS),
			keyVal("host.name", hostname),
		},
	}
}

func (s *Supervisor) loadAgentEffectiveConfig() error {
	var effectiveConfigBytes []byte

	effFromFile, err := os.ReadFile(s.effectiveConfigFilePath)
	if err == nil {
		// We have an effective config file.
		effectiveConfigBytes = effFromFile
	} else {
		// No effective config file, just use the initial config.
		effectiveConfigBytes = []byte(localOverrideAgentConfig)
	}

	s.effectiveConfig.Store(string(effectiveConfigBytes))

	return nil
}

// createEffectiveConfigMsg create an EffectiveConfig with the content of the
// current effective config.
func (s *Supervisor) createEffectiveConfigMsg() *protobufs.EffectiveConfig {
	cfgStr, ok := s.effectiveConfig.Load().(string)
	if !ok {
		cfgStr = ""
	}

	hash := sha256.Sum256([]byte(cfgStr))
	hashBytes := hash[:]

	cfg := &protobufs.EffectiveConfig{
		Hash: hashBytes,
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"": {Body: []byte(cfgStr)},
			},
		},
	}

	return cfg
}

// onRemoteConfig processes a remote config message received from OpAMP Server.
func (s *Supervisor) onRemoteConfig(
	_ context.Context,
	remoteConfig *protobufs.AgentRemoteConfig,
) (effectiveConfig *protobufs.EffectiveConfig, configChanged bool, err error) {
	if remoteConfig == nil {
		return nil, false, nil
	}

	s.remoteConfig = remoteConfig
	s.logger.Debugf("Received remote config from server, hash=%x.", s.remoteConfig.ConfigHash)

	configChanged, err = s.recalcEffectiveConfig()
	if err != nil {
		return nil, false, err
	}

	return s.createEffectiveConfigMsg(), configChanged, nil
}

// onOwnTelemetryConnectionSettings processes a own telemetry settings message
// received from OpAMP Server.
func (s *Supervisor) onOwnTelemetryConnectionSettings(
	ctx context.Context,
	telemetryType types.OwnTelemetryType,
	settings *protobufs.TelemetryConnectionSettings,
) error {
	switch telemetryType {
	case types.OwnMetrics:
		s.setupOwnMetrics(ctx, settings)
	}

	return nil
}

// onAgentIdentificationFunc processes new identification message
// received from OpAMP Server.
func (s *Supervisor) onAgentIdentificationFunc(
	_ context.Context,
	agentId *protobufs.AgentIdentification,
) error {
	newInstanceId, err := ulid.Parse(agentId.NewInstanceUid)
	if err != nil {
		return err
	}

	s.logger.Debugf("Agent identify is being changed from id=%v to id=%v",
		s.instanceId.String(),
		newInstanceId.String())
	s.instanceId = newInstanceId

	// TODO: update metrics pipeline.
	return nil
}

func (s *Supervisor) setupOwnMetrics(ctx context.Context, settings *protobufs.TelemetryConnectionSettings) {
	var cfg string
	if settings.DestinationEndpoint == "" {
		// No destination. Disable metric collection.
		cfg = ""
	} else {
		// TODO: choose the scraping port dynamically instead of hard-coding to 8888.
		cfg = fmt.Sprintf(
			`
receivers:
  # Collect own metrics
  prometheus/own_metrics:
    config:
      scrape_configs:
        - job_name: 'otel-collector'
          scrape_interval: 10s
          static_configs:
            - targets: ['0.0.0.0:8888']  
exporters:
  otlphttp/own_metrics:
    endpoint: %s

service:
  pipelines:
    metrics/own_metrics:
      receivers: [prometheus/own_metrics]
      exporters: [otlphttp/own_metrics]
`, settings.DestinationEndpoint,
		)
	}

	s.agentConfigOwnMetricsSection.Store(cfg)

	// Need to recalculate the Agent config so that the metric config is included in it.
	configChanged, err := s.recalcEffectiveConfig()
	if err != nil {
		return
	}

	if configChanged {
		// TODO: make explicit in the OpAMPClient contract that we are allowed to call
		// UpdateEffectiveConfig from the callbacks (like it is done here).
		// This currently works, but is not ideal since it may result in 2 messages sent:
		// one as a result of this call and another when the callback returns.
		s.opampClient.UpdateEffectiveConfig(ctx)
	}
}

// composeEffectiveConfig composes the effective config from multiple sources:
// 1) the remote config from OpAMP Server, 2) the own metrics config section,
// 3) the local override config that is hard-coded in the Supervisor.
func (s *Supervisor) composeEffectiveConfig(config *protobufs.AgentRemoteConfig) (configChanged bool, err error) {
	var k = koanf.New(".")

	// Begin with empty config. We will merge received configs on top of it.
	if err := k.Load(rawbytes.Provider([]byte{}), yaml.Parser()); err != nil {
		return false, err
	}

	// Sort to make sure the order of merging is stable.
	var names []string
	for name := range config.Config.ConfigMap {
		if name == "" {
			// skip instance config
			continue
		}
		names = append(names, name)
	}

	sort.Strings(names)

	// Append instance config as the last item.
	names = append(names, "")

	// Merge received configs.
	for _, name := range names {
		item := config.Config.ConfigMap[name]
		var k2 = koanf.New(".")
		err := k2.Load(rawbytes.Provider(item.Body), yaml.Parser())
		if err != nil {
			return false, fmt.Errorf("cannot parse config named %s: %v", name, err)
		}
		err = k.Merge(k2)
		if err != nil {
			return false, fmt.Errorf("cannot merge config named %s: %v", name, err)
		}
	}

	// Merge own metrics config.
	ownMetricsCfg, ok := s.agentConfigOwnMetricsSection.Load().(string)
	if ok {
		if err := k.Load(rawbytes.Provider([]byte(ownMetricsCfg)), yaml.Parser()); err != nil {
			return false, err
		}
	}

	// Merge local config last since it has the highest precedence.
	if err := k.Load(rawbytes.Provider([]byte(localOverrideAgentConfig)), yaml.Parser()); err != nil {
		return false, err
	}

	// The merged final result is our effective config.
	effectiveConfigBytes, err := k.Marshal(yaml.Parser())
	if err != nil {
		return false, err
	}

	// Check if effective config is changed.
	newEffectiveConfig := string(effectiveConfigBytes)
	configChanged = false
	if s.effectiveConfig.Load().(string) != newEffectiveConfig {
		s.logger.Debugf("Effective config changed.")
		s.effectiveConfig.Store(newEffectiveConfig)
		configChanged = true
	}

	return configChanged, nil
}

// Recalculate the Agent's effective config and if the config changes signal to the
// background goroutine that the config needs to be applied to the Agent.
func (s *Supervisor) recalcEffectiveConfig() (configChanged bool, err error) {

	configChanged, err = s.composeEffectiveConfig(s.remoteConfig)
	if err != nil {
		s.logger.Errorf("Error composing effective config. Ignoring received config: %v", err)
		return configChanged, err
	}

	// TODO: delay the application of this config in case there is another callback coming
	// that will change the connection settings and will result in effective config
	// change again. Currently we unnecessarily restart the agent twice.

	if configChanged {
		s.logger.Debugf("Config is changed. Signal to restart the agent.")
		// Signal that there is a new config.
		select {
		case s.hasNewConfig <- struct{}{}:
		default:
		}
	} else {
		s.logger.Debugf("Config is unchanged, nothing to do.")
	}

	return configChanged, nil
}

func (s *Supervisor) startAgent() {
	err := s.commander.Start(context.Background())
	if err != nil {
		s.logger.Errorf("Cannot startOpAMP the agent: %v", err)
	}
}

func (s *Supervisor) runAgentProcess() {
	if _, err := os.Stat(s.effectiveConfigFilePath); err == nil {
		// We have an effective config file saved previously. Use it to start the agent.
		s.startAgent()
	}

	restartTimer := time.NewTimer(0)
	restartTimer.Stop()

	for {
		select {
		case <-s.hasNewConfig:
			restartTimer.Stop()
			s.applyConfigWithAgentRestart()

		case <-s.commander.Done():
			s.logger.Debugf(
				"Agent process PID=%d exited unexpectedly, exit code=%d. Will restart in a bit...",
				s.commander.Pid(), s.commander.ExitCode())

			// TODO: decide why the agent stopped. If it was due to bad config, report it to server.

			// Wait 5 seconds before starting again.
			restartTimer.Stop()
			restartTimer.Reset(5 * time.Second)

		case <-restartTimer.C:
			s.startAgent()
		}
	}
}

func (s *Supervisor) applyConfigWithAgentRestart() {
	s.logger.Debugf("Restarting the agent with the new config.")
	cfg := s.effectiveConfig.Load().(string)
	s.commander.Stop(context.Background())
	s.writeEffectiveConfigToFile(cfg, s.effectiveConfigFilePath)
	s.startAgent()
}

func (s *Supervisor) writeEffectiveConfigToFile(cfg string, filePath string) {
	f, err := os.Create(filePath)
	if err != nil {
		s.logger.Errorf("Cannot write effective config file: %v", err)
	}
	defer f.Close()

	f.WriteString(cfg)
}

func (s *Supervisor) Shutdown() {
	s.logger.Debugf("Supervisor shutting down...")
	if s.opampClient != nil {
		_ = s.opampClient.Stop(context.Background())
	}
}
