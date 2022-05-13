package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// Agent represents a connected Agent.
type Agent struct {
	// Some fields in this struct are exported so that we can render them in the UI.

	// Agent's instance id. This is an immutable field.
	InstanceId InstanceId

	// Connection to the Agent.
	conn types.Connection
	// Mutex to protect Send() operation.
	connMutex sync.Mutex

	// mutex for the fields that follow it.
	mux sync.RWMutex

	// Agent's current status.
	Status *protobufs.StatusReport

	// Effective config reported by the Agent.
	EffectiveConfig string

	// Optional special remote config for this particular instance defined by
	// the user in the UI.
	CustomInstanceConfig string

	// Remote config that we will give to this Agent.
	remoteConfig *protobufs.AgentRemoteConfig

	// Channels to notify when this Agent's status is updated next time.
	statusUpdateWatchers []chan<- struct{}
}

func NewAgent(
	instanceId InstanceId,
	conn types.Connection,
) *Agent {
	return &Agent{InstanceId: instanceId, conn: conn}
}

// CloneReadonly returns a copy of the Agent that is safe to read.
// Functions that modify the Agent should not be called on the cloned copy.
func (agent *Agent) CloneReadonly() *Agent {
	agent.mux.RLock()
	defer agent.mux.RUnlock()
	return &Agent{
		InstanceId:           agent.InstanceId,
		Status:               proto.Clone(agent.Status).(*protobufs.StatusReport),
		EffectiveConfig:      agent.EffectiveConfig,
		CustomInstanceConfig: agent.CustomInstanceConfig,
		remoteConfig:         proto.Clone(agent.remoteConfig).(*protobufs.AgentRemoteConfig),
	}
}

// UpdateStatus updates the status of the Agent struct based on the newly received
// status report and sets appropriate fields in the response message to be sent
// to the Agent.
func (agent *Agent) UpdateStatus(
	newStatus *protobufs.StatusReport,
	response *protobufs.ServerToAgent,
) {
	agent.mux.Lock()

	agent.processStatusUpdate(newStatus, response)

	statusUpdateWatchers := agent.statusUpdateWatchers
	agent.statusUpdateWatchers = nil

	agent.mux.Unlock()

	// Notify watcher outside mutex to avoid blocking the mutex for too long.
	notifyStatusWatchers(statusUpdateWatchers)
}

func notifyStatusWatchers(statusUpdateWatchers []chan<- struct{}) {
	// Notify everyone who is waiting on this Agent's status updates.
	for _, ch := range statusUpdateWatchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (agent *Agent) updateStatusField(newStatus *protobufs.StatusReport) (agentDescrChanged bool) {
	prevStatus := agent.Status

	if agent.Status == nil {
		// First time this Agent reports a status, remember it.
		agent.Status = newStatus
		agentDescrChanged = true
	} else {
		// Not a new Agent. Checks what's changed in the Agent's description.
		if newStatus.AgentDescription != nil {
			// If the AgentDescription field is set it means the Agent tells us
			// something is changed in the field since the last status report
			// (or this is the first report).
			// Make full comparison of previous and new descriptions to see if it
			// really is different.
			if prevStatus != nil && isEqualAgentDescr(prevStatus.AgentDescription, newStatus.AgentDescription) {
				// Agent description didn't change.
				agentDescrChanged = false
			} else {
				// Yes, the description is different, update it.
				agent.Status.AgentDescription = newStatus.AgentDescription
				agentDescrChanged = true
			}
		} else {
			// AgentDescription field is not set, which means description didn't change.
			agentDescrChanged = false
		}

		// Update remote config status if it is included and is different from what we have.
		if newStatus.RemoteConfigStatus != nil &&
			!bytes.Equal(agent.Status.RemoteConfigStatus.Hash, newStatus.RemoteConfigStatus.Hash) {

			if newStatus.RemoteConfigStatus.Status == protobufs.RemoteConfigStatus_UNSET {
				// TODO: Request full RemoteConfigStatus using
				// ServerToAgent_ReportRemoteConfigStatus flag.
			} else {
				agent.Status.RemoteConfigStatus = newStatus.RemoteConfigStatus
			}
		}
	}

	return agentDescrChanged
}

func (agent *Agent) updateEffectiveConfig(
	newStatus *protobufs.StatusReport,
	response *protobufs.ServerToAgent,
) {
	// Update effective config if provided.
	if newStatus.EffectiveConfig != nil {
		if newStatus.EffectiveConfig.ConfigMap != nil {
			agent.Status.EffectiveConfig = newStatus.EffectiveConfig

			// Convert to string for displaying purposes.
			agent.EffectiveConfig = ""
			for _, cfg := range newStatus.EffectiveConfig.ConfigMap.ConfigMap {
				// TODO: we just concatenate parts of effective config as a single
				// blob to show in the UI. A proper approach is to keep the effective
				// config as a set and show the set in the UI.
				agent.EffectiveConfig = agent.EffectiveConfig + string(cfg.Body)
			}
		}
	}

	if agent.Status.EffectiveConfig == nil ||
		newStatus.EffectiveConfig == nil ||
		agent.Status.EffectiveConfig.ConfigMap == nil ||
		!bytes.Equal(agent.Status.EffectiveConfig.Hash, newStatus.EffectiveConfig.Hash) {
		// Ask the Agent to report back the effective config since we don't have it
		// or what we have is different from what the Agent has because hashes don't match.
		response.Flags = response.Flags | protobufs.ServerToAgent_ReportEffectiveConfig
	}
}

func (agent *Agent) processStatusUpdate(
	newStatus *protobufs.StatusReport,
	response *protobufs.ServerToAgent,
) {
	agentDescrChanged := agent.updateStatusField(newStatus)

	configChanged := false
	if agentDescrChanged {
		// Agent description is changed.
		//
		// We need to recalculate the config.
		configChanged = agent.calcRemoteConfig()

		// And set connection settings that are appropriate for the Agent description.
		agent.calcConnectionSettings(response)
	}

	// If remote config is changed and different from what the Agent has then
	// send the new remote config to the Agent.
	if configChanged ||
		(newStatus.RemoteConfigStatus != nil &&
			bytes.Compare(newStatus.RemoteConfigStatus.LastRemoteConfigHash, agent.remoteConfig.ConfigHash) != 0) {
		// The new status resulted in a change in the config of the Agent or the Agent
		// does not have this config (hash is different). Send the new config the Agent.
		response.RemoteConfig = agent.remoteConfig
	}

	agent.updateEffectiveConfig(newStatus, response)
}

// SetCustomConfig sets a custom config for this Agent.
// notifyWhenConfigIsApplied channel is notified after the remote config is applied
// to the Agent and after the Agent reports back the effective config.
// If the provided config is equal to the current remoteConfig of the Agent
// then we will not send any config to the Agent and notifyWhenConfigIsApplied channel
// will be notified immediately. This requires that notifyWhenConfigIsApplied channel
// has a buffer size of at least 1.
func (agent *Agent) SetCustomConfig(
	config *protobufs.AgentConfigMap,
	notifyWhenConfigIsApplied chan<- struct{},
) {
	agent.mux.Lock()

	agent.CustomInstanceConfig = string(config.ConfigMap[""].Body)

	configChanged := agent.calcRemoteConfig()
	if configChanged {
		if notifyWhenConfigIsApplied != nil {
			// The caller wants to be notified when the Agent reports a status
			// update next time. This is typically used in the UI to wait until
			// the configuration changes are propagated successfully to the Agent.
			agent.statusUpdateWatchers = append(
				agent.statusUpdateWatchers,
				notifyWhenConfigIsApplied,
			)
		}
		msg := &protobufs.ServerToAgent{
			RemoteConfig: agent.remoteConfig,
		}
		agent.mux.Unlock()

		agent.SendToAgent(msg)
	} else {
		agent.mux.Unlock()

		if notifyWhenConfigIsApplied != nil {
			// No config change. We are not going to send config to the Agent and
			// as a result we do not expect status update from the Agent, so we will
			// just notify the waiter that the config change is done.
			notifyWhenConfigIsApplied <- struct{}{}
		}
	}
}

// calcRemoteConfig calculates the remote config for this Agent. It returns true if
// the calculated new config is different from the existing config stored in
// Agent.remoteConfig.
func (agent *Agent) calcRemoteConfig() bool {
	hash := sha256.New()

	cfg := protobufs.AgentRemoteConfig{
		Config: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{},
		},
	}

	// Add the custom config for this particular Agent instance. Use empty
	// string as the config file name.
	cfg.Config.ConfigMap[""] = &protobufs.AgentConfigFile{
		Body: []byte(agent.CustomInstanceConfig),
	}

	// Calculate the hash.
	for k, v := range cfg.Config.ConfigMap {
		hash.Write([]byte(k))
		hash.Write(v.Body)
		hash.Write([]byte(v.ContentType))
	}

	cfg.ConfigHash = hash.Sum(nil)

	configChanged := !isEqualRemoteConfig(agent.remoteConfig, &cfg)

	agent.remoteConfig = &cfg

	return configChanged
}

func isEqualRemoteConfig(c1, c2 *protobufs.AgentRemoteConfig) bool {
	if c1 == c2 {
		return true
	}
	if c1 == nil || c2 == nil {
		return false
	}
	return isEqualConfigSet(c1.Config, c2.Config)
}

func isEqualConfigSet(c1, c2 *protobufs.AgentConfigMap) bool {
	if c1 == c2 {
		return true
	}
	if c1 == nil || c2 == nil {
		return false
	}
	if len(c1.ConfigMap) != len(c2.ConfigMap) {
		return false
	}
	for k, v1 := range c1.ConfigMap {
		v2, ok := c2.ConfigMap[k]
		if !ok {
			return false
		}
		if !isEqualConfigFile(v1, v2) {
			return false
		}
	}
	return true
}

func isEqualConfigFile(f1, f2 *protobufs.AgentConfigFile) bool {
	if f1 == f2 {
		return true
	}
	if f1 == nil || f2 == nil {
		return false
	}
	return bytes.Compare(f1.Body, f2.Body) == 0 && f1.ContentType == f2.ContentType
}

func (agent *Agent) calcConnectionSettings(response *protobufs.ServerToAgent) {
	// Here we can use Agent's description to send the appropriate connection
	// settings to the Agent.
	// In this simple example the connection settings do not depend on the
	// Agent description, so we jst set them directly.

	response.ConnectionSettings = &protobufs.ConnectionSettingsOffers{
		Hash:  nil, // TODO: calc has from settings.
		Opamp: nil,
		OwnMetrics: &protobufs.ConnectionSettings{
			// We just hard-code this to a port on a localhost on which we can
			// run an Otel Collector for demo purposes. With real production
			// servers this should likely point to an OTLP backend.
			DestinationEndpoint: "http://localhost:4318/v1/metrics",
		},
		OwnTraces:        nil,
		OwnLogs:          nil,
		OtherConnections: nil,
	}
}

func (agent *Agent) SendToAgent(msg *protobufs.ServerToAgent) {
	agent.connMutex.Lock()
	defer agent.connMutex.Unlock()

	agent.conn.Send(context.Background(), msg)
}
