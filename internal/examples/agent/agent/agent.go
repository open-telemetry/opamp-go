package agent

import (
	"context"
	"crypto/sha256"
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
	"github.com/open-telemetry/opamp-go/client/types"

	"github.com/open-telemetry/opamp-go/client"
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

	effectiveConfig     string
	effectiveConfigHash []byte

	instanceId ulid.ULID

	agentDescription *protobufs.AgentDescription

	opampClient client.OpAMPClient

	remoteConfigHash []byte
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
	if err := agent.start(); err != nil {
		agent.logger.Errorf("Cannot start OpAMP client: %v", err)
		return nil
	}

	return agent
}

func (agent *Agent) start() error {
	agent.opampClient = client.New(agent.logger)

	settings := client.StartSettings{
		OpAMPServerURL:   "ws://127.0.0.1:4320/v1/opamp",
		InstanceUid:      agent.instanceId.String(),
		AgentDescription: agent.agentDescription,
		Callbacks: client.CallbacksStruct{
			OnConnectFunc: func() {
				agent.logger.Debugf("Connected to the server.")
			},
			OnConnectFailedFunc: func(err error) {
				agent.logger.Errorf("Failed to connect to the server: %v", err)
			},
			OnErrorFunc: func(err *protobufs.ServerErrorResponse) {
				agent.logger.Errorf("Server returned an error response: %v", err.ErrorMessage)
			},
			OnRemoteConfigFunc: agent.onRemoteConfig,
		},
		LastRemoteConfigHash: agent.remoteConfigHash,
		LastEffectiveConfig:  agent.composeEffectiveConfig(),
	}

	agent.logger.Debugf("Starting OpAMP client...")

	err := agent.opampClient.Start(settings)
	if err != nil {
		return err
	}

	agent.logger.Debugf("OpAMP Client started.")

	return nil
}

func (agent *Agent) createAgentIdentity() {
	// Generate instance id.
	entropy := ulid.Monotonic(rand.New(rand.NewSource(0)), 0)
	agent.instanceId = ulid.MustNew(ulid.Timestamp(time.Now()), entropy)

	hostname, _ := os.Hostname()

	// Create agent description.
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
				Key: "os.family",
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

func (agent *Agent) loadLocalConfig() {
	var k = koanf.New(".")
	k.Load(rawbytes.Provider([]byte(localConfig)), yaml.Parser())

	effectiveConfigBytes, err := k.Marshal(yaml.Parser())
	if err != nil {
		panic(err)
	}

	agent.effectiveConfig = string(effectiveConfigBytes)
	hash := sha256.Sum256(effectiveConfigBytes)
	agent.effectiveConfigHash = hash[:]
}

func (agent *Agent) composeEffectiveConfig() *protobufs.EffectiveConfig {
	return &protobufs.EffectiveConfig{
		Hash: agent.effectiveConfigHash,
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"": {Body: []byte(agent.effectiveConfig)},
			},
		},
	}
}

func (agent *Agent) onRemoteConfig(
	ctx context.Context,
	config *protobufs.AgentRemoteConfig,
) (*protobufs.EffectiveConfig, error) {
	err := agent.applyRemoteConfig(config)
	if err != nil {
		return nil, err
	}
	return agent.composeEffectiveConfig(), nil
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

func (agent *Agent) applyRemoteConfig(config *protobufs.AgentRemoteConfig) error {
	if config == nil {
		return nil
	}

	agent.logger.Debugf("Received remote config from server, hash=%x.", config.ConfigHash)

	// Begin with local config. We will later merge received configs on top of it.
	var k = koanf.New(".")
	if err := k.Load(rawbytes.Provider([]byte(localConfig)), yaml.Parser()); err != nil {
		return err
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
			return fmt.Errorf("cannot parse config named %s: %v", item.name, err)
		}
		err = k.Merge(k2)
		if err != nil {
			return fmt.Errorf("cannot merge config named %s: %v", item.name, err)
		}
	}

	// The merged final result is our effective config.
	effectiveConfigBytes, err := k.Marshal(yaml.Parser())
	if err != nil {
		panic(err)
	}

	newEffectiveConfig := string(effectiveConfigBytes)
	if agent.effectiveConfig != newEffectiveConfig {
		agent.logger.Debugf("Effective config changed. Need to report to server.")
		agent.effectiveConfig = newEffectiveConfig
		hash := sha256.Sum256(effectiveConfigBytes)
		agent.effectiveConfigHash = hash[:]
	}

	agent.remoteConfigHash = config.ConfigHash

	return nil
}

func (agent *Agent) Shutdown() {
	agent.logger.Debugf("Agent shutting down...")
	if agent.opampClient != nil {
		agent.opampClient.Stop(context.Background())
	}
}
