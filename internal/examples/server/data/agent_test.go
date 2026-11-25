package data

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestUpdateStatusRemoteConfigHash(t *testing.T) {
	for _, test := range []struct {
		name        string
		hashMatches bool
	}{
		{name: "matching hash", hashMatches: true},
		{name: "different hash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := Agent{CustomInstanceConfig: "receivers: {}"}
			agent.calcRemoteConfig()

			reportedHash := []byte("previous configuration hash")
			if test.hashMatches {
				reportedHash = agent.remoteConfig.ConfigHash
			}

			response := &protobufs.ServerToAgent{}
			agent.UpdateStatus(&protobufs.AgentToServer{
				Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig),
				RemoteConfigStatus: &protobufs.RemoteConfigStatus{
					LastRemoteConfigHash: reportedHash,
				},
			}, response)

			if test.hashMatches {
				require.Nil(t, response.RemoteConfig)
			} else {
				require.NotNil(t, response.RemoteConfig)
				require.Equal(t, agent.remoteConfig.ConfigHash, response.RemoteConfig.ConfigHash)
			}
		})
	}
}
