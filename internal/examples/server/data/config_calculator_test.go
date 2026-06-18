package data

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalcRemoteConfigAppliesCalculatorsInOrder(t *testing.T) {
	agent := NewAgent(
		InstanceId{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		&recordingConnection{},
	)
	agent.WithConfigCalculator(appendCalculator(" first"))
	agent.WithConfigCalculator(appendCalculator(" second"))
	agent.CustomInstanceConfig = "base"

	changed := agent.calcRemoteConfig()

	require.True(t, changed)
	assert.Equal(t, "base first second", string(agent.remoteConfig.Config.ConfigMap[""].Body))
}

type appendCalculator string

func (calculator appendCalculator) Calculate(_ *Agent, configBody string) string {
	return configBody + string(calculator)
}

func (calculator appendCalculator) AgentsChanged(_ map[InstanceId]*Agent) map[InstanceId]bool {
	return nil
}
