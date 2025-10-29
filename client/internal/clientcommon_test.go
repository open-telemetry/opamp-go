package internal

import (
	"testing"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
)

func TestClientCommon_SetCapabilities(t *testing.T) {
	tests := []struct {
		name          string
		capabilities  protobufs.AgentCapabilities
		expectedError error
	}{
		{name: "empty", capabilities: 0, expectedError: nil},
		{name: "package capabilities", capabilities: protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses | protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages, expectedError: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClientCommon(nil, NewMockSender())
			err := client.SetCapabilities(&test.capabilities)
			assert.Equal(t, test.expectedError, err)
		})
	}
}
