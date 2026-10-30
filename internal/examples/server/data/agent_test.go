package data

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestProcessStatusUpdateSendsRemoteConfigOnlyWhenAgentHashDiffers(t *testing.T) {
	agent := &Agent{}
	seq := uint64(0)

	// report sends a status with a changed AgentDescription, which is what makes
	// the server reconsider the remote config, and the given last applied hash.
	report := func(lastRemoteConfigHash []byte) *protobufs.ServerToAgent {
		seq++
		status := &protobufs.AgentToServer{
			SequenceNum:  seq,
			Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig),
			AgentDescription: &protobufs.AgentDescription{
				IdentifyingAttributes: []*protobufs.KeyValue{{
					Key:   "seq",
					Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_IntValue{IntValue: int64(seq)}},
				}},
			},
		}
		if lastRemoteConfigHash != nil {
			status.RemoteConfigStatus = &protobufs.RemoteConfigStatus{LastRemoteConfigHash: lastRemoteConfigHash}
		}
		response := &protobufs.ServerToAgent{}
		agent.processStatusUpdate(status, response)
		return response
	}

	// First report: the agent has no config yet, so it is sent.
	response := report(nil)
	require.NotNil(t, response.RemoteConfig)
	currentHash := response.RemoteConfig.ConfigHash

	// The agent already has the current config: nothing to send.
	response = report(currentHash)
	assert.Nil(t, response.RemoteConfig)

	// The agent has a different config: the current one is sent again.
	response = report([]byte("stale"))
	require.NotNil(t, response.RemoteConfig)
	assert.Equal(t, currentHash, response.RemoteConfig.ConfigHash)
}
