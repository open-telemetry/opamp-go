package data

import (
	"sync"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/protobufshelpers"
	"github.com/open-telemetry/opamp-go/server/types"
)

type Agents struct {
	mux         sync.RWMutex
	agentsById  map[InstanceId]*Agent
	connections map[types.Connection]map[InstanceId]bool
}

// RemoveConnection removes the connection all agent instances associated with the
// connection.
func (agents *Agents) RemoveConnection(conn types.Connection) {
	agents.mux.Lock()
	defer agents.mux.Unlock()

	for instanceId := range agents.connections[conn] {
		delete(agents.agentsById, instanceId)
	}
	delete(agents.connections, conn)
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

func isEqualAgentDescr(d1, d2 *protobufs.AgentDescription) bool {
	if d1 == d2 {
		return true
	}
	if d1 == nil || d2 == nil {
		return false
	}
	if d1.AgentType != d2.AgentType || d1.AgentVersion != d2.AgentVersion {
		return false
	}
	if len(d1.AgentAttributes) != len(d2.AgentAttributes) {
		return false
	}
	for i, a1 := range d1.AgentAttributes {
		a2 := d2.AgentAttributes[i]
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

	// Ensure the agent is in the agentsById map.
	agent := agents.agentsById[agentId]
	if agent == nil {
		agent = NewAgent(agentId, conn)
		agents.agentsById[agentId] = agent

		// Ensure the agent's instance id is associated with the connection.
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
	agents.mux.RLock()

	// Clone the map first
	m := map[InstanceId]*Agent{}
	for id, agent := range agents.agentsById {
		m[id] = agent
	}
	agents.mux.RUnlock()

	// Clone agents in the map
	for id, agent := range m {
		// Return a clone to allow safe access after returning.
		m[id] = agent.CloneReadonly()
	}
	return m
}

var AllAgents = Agents{
	agentsById:  map[InstanceId]*Agent{},
	connections: map[types.Connection]map[InstanceId]bool{},
}
