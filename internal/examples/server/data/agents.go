package data

import (
	"crypto/sha256"
	"io"
	"log"
	"sync"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/protobufshelpers"
	"github.com/open-telemetry/opamp-go/server/types"
)

type Agents struct {
	mux               sync.RWMutex
	agentsById        map[InstanceId]*Agent
	connections       map[types.Connection]map[InstanceId]bool
	configCalculators []ConfigCalculator
}

var logger = log.New(log.Default().Writer(), "[AGENTS] ", log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds)

// RemoveConnection removes the connection all Agent instances associated with the
// connection.
func (agents *Agents) RemoveConnection(conn types.Connection) {
	agents.mux.Lock()
	for instanceId := range agents.connections[conn] {
		delete(agents.agentsById, instanceId)
	}
	delete(agents.connections, conn)
	agents.mux.Unlock()

	agents.AgentsChanged(nil, nil)
}

func (agents *Agents) SendCustomMessageToAgent(
	agentId InstanceId,
	customMsg *protobufs.ServerToAgent,
) {
	agent := agents.FindAgent(agentId)
	if agent != nil {
		agent.SendCustomMessage(customMsg)
	}
}

func (agents *Agents) SetCustomConfigForAgent(
	agentId InstanceId,
	config *protobufs.AgentConfigMap,
	notifyNextStatusUpdate chan<- struct{},
) {
	agent := agents.FindAgent(agentId)
	if agent != nil {
		agent.SetCustomConfig(config, notifyNextStatusUpdate)
	}
}

func (agents *Agents) AgentsChanged(
	currentAgent *Agent,
	currentResponse *protobufs.ServerToAgent,
) {
	allAgents := agents.getAllAgents()

	changedAgents := map[InstanceId]bool{}
	for _, calculator := range agents.configCalculators {
		for instanceID := range calculator.AgentsChanged(allAgents) {
			changedAgents[instanceID] = true
		}
	}

	agents.recalculateRemoteConfigs(changedAgents, allAgents, currentAgent, currentResponse)
}

func (agents *Agents) recalculateRemoteConfigs(
	changedAgents map[InstanceId]bool,
	allAgents map[InstanceId]*Agent,
	currentAgent *Agent,
	currentResponse *protobufs.ServerToAgent,
) {
	var updates []remoteConfigUpdate
	for instanceID := range changedAgents {
		agent := allAgents[instanceID]
		if agent == nil {
			continue
		}

		remoteConfig, changed := agent.RecalculateRemoteConfig()
		if !changed {
			continue
		}
		if agent == currentAgent && currentResponse != nil {
			currentResponse.RemoteConfig = remoteConfig
			continue
		}
		updates = append(updates, remoteConfigUpdate{
			agent:        agent,
			remoteConfig: remoteConfig,
		})
	}

	for _, update := range updates {
		update.agent.SendToAgent(&protobufs.ServerToAgent{
			RemoteConfig: update.remoteConfig,
		})
	}
}

type remoteConfigUpdate struct {
	agent        *Agent
	remoteConfig *protobufs.AgentRemoteConfig
}

func isEqualAgentDescr(d1, d2 *protobufs.AgentDescription) bool {
	if d1 == d2 {
		return true
	}
	if d1 == nil || d2 == nil {
		return false
	}
	return isEqualAttrs(d1.IdentifyingAttributes, d2.IdentifyingAttributes) &&
		isEqualAttrs(d1.NonIdentifyingAttributes, d2.NonIdentifyingAttributes)
}

func isEqualAttrs(attrs1, attrs2 []*protobufs.KeyValue) bool {
	if len(attrs1) != len(attrs2) {
		return false
	}
	for i, a1 := range attrs1 {
		a2 := attrs2[i]
		if !protobufshelpers.IsEqualKeyValue(a1, a2) {
			return false
		}
	}
	return true
}

func (agents *Agents) FindAgent(agentId InstanceId) *Agent {
	agents.mux.RLock()
	defer agents.mux.RUnlock()
	return agents.agentsById[agentId]
}

func (agents *Agents) FindOrCreateAgent(agentId InstanceId, conn types.Connection) *Agent {
	agents.mux.Lock()
	defer agents.mux.Unlock()

	// Ensure the Agent is in the agentsById map.
	agent := agents.agentsById[agentId]
	if agent == nil {
		agent = NewAgent(agentId, conn)
		for _, calculator := range agents.configCalculators {
			agent.WithConfigCalculator(calculator)
		}
		agents.agentsById[agentId] = agent

		// Ensure the Agent's instance id is associated with the connection.
		if agents.connections[conn] == nil {
			agents.connections[conn] = map[InstanceId]bool{}
		}
		agents.connections[conn][agentId] = true
	}

	return agent
}

func (agents *Agents) GetAgentReadonlyClone(agentId InstanceId) *Agent {
	agent := agents.FindAgent(agentId)
	if agent == nil {
		return nil
	}

	// Return a clone to allow safe access after returning.
	return agent.CloneReadonly()
}

func (agents *Agents) GetAllAgentsReadonlyClone() map[InstanceId]*Agent {
	m := agents.getAllAgents()

	// Clone agents in the map
	for id, agent := range m {
		// Return a clone to allow safe access after returning.
		m[id] = agent.CloneReadonly()
	}
	return m
}

func (agents *Agents) getAllAgents() map[InstanceId]*Agent {
	agents.mux.RLock()
	defer agents.mux.RUnlock()

	m := map[InstanceId]*Agent{}
	for id, agent := range agents.agentsById {
		m[id] = agent
	}
	return m
}

func (a *Agents) OfferAgentConnectionSettings(
	id InstanceId,
	offers *protobufs.ConnectionSettingsOffers,
) {
	if len(offers.Hash) == 0 {
		offers.Hash = toHash(offers)
	}

	a.mux.Lock()
	defer a.mux.Unlock()

	agent, ok := a.agentsById[id]
	if ok {
		agent.OfferConnectionSettings(offers)
		logger.Printf("Client connection settings offers sent to %s (hash=%x)\n", id, offers.Hash)
	} else {
		logger.Printf("Agent %s not found\n", id)
	}
}

func NewAgents(configCalculators ...ConfigCalculator) *Agents {
	return &Agents{
		agentsById:        map[InstanceId]*Agent{},
		connections:       map[types.Connection]map[InstanceId]bool{},
		configCalculators: configCalculators,
	}
}

var AllAgents = NewAgents()

// toHash computes a sha256 hash from fields within ConnectionSettingsOffers
func toHash(c *protobufs.ConnectionSettingsOffers) []byte {
	hasher := sha256.New()
	if c.Opamp != nil {
		hashEndpoint(hasher, c.Opamp)
	}
	if c.OwnMetrics != nil {
		hashEndpoint(hasher, c.OwnMetrics)
	}
	if c.OwnTraces != nil {
		hashEndpoint(hasher, c.OwnTraces)
	}
	if c.OwnLogs != nil {
		hashEndpoint(hasher, c.OwnLogs)
	}
	for name, endpoint := range c.OtherConnections {
		hasher.Write([]byte(name))
		hashEndpoint(hasher, endpoint)
	}

	return hasher.Sum(nil)
}

// endpoint is an incomplete interface to assist with turning an offered connection into a hash.
type endpoint interface {
	GetDestinationEndpoint() string
	GetHeaders() *protobufs.Headers
	GetCertificate() *protobufs.TLSCertificate
	GetTls() *protobufs.TLSConnectionSettings
}

// hashEndpoint writes some shared attributes of the passed enpoint to the passed writer.
func hashEndpoint(w io.Writer, e endpoint) {
	_, err := w.Write([]byte(e.GetDestinationEndpoint()))
	if err != nil {
		panic(err)
	}
	if headers := e.GetHeaders(); headers != nil {
		for _, header := range headers.Headers {
			_, err := w.Write([]byte(header.Key + header.Value))
			if err != nil {
				panic(err)
			}
		}
	}
	if cert := e.GetCertificate(); cert != nil {
		_, err := w.Write(cert.Cert)
		if err != nil {
			panic(err)
		}
		_, err = w.Write(cert.PrivateKey)
		if err != nil {
			panic(err)
		}
		_, err = w.Write(cert.CaCert)
		if err != nil {
			panic(err)
		}
	}
	if tlsSettings := e.GetTls(); tlsSettings != nil {
		_, err := w.Write([]byte(tlsSettings.CaPemContents))
		if err != nil {
			panic(err)
		}
	}
}
