package internal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestCommandStatusReporting(t *testing.T) {
	commandID := []byte("command-1")

	tests := []struct {
		name         string
		capabilities protobufs.AgentCapabilities
		commandErr   error
		// Status observed from inside OnCommand.
		duringStatus *protobufs.CommandStatus
		// Status in the state and the pending message after OnCommand returns.
		afterStatus *protobufs.CommandStatus
	}{
		{
			name:         "capability not set",
			capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand,
		},
		{
			name: "command succeeds",
			capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand |
				protobufs.AgentCapabilities_AgentCapabilities_ReportsCommandStatus,
			duringStatus: &protobufs.CommandStatus{
				CommandId: commandID,
				Type:      protobufs.CommandType_CommandType_Restart,
				Status:    protobufs.CommandStatuses_CommandStatuses_EXECUTING,
			},
			afterStatus: &protobufs.CommandStatus{
				CommandId: commandID,
				Type:      protobufs.CommandType_CommandType_Restart,
				Status:    protobufs.CommandStatuses_CommandStatuses_EXECUTING,
			},
		},
		{
			name: "command fails",
			capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand |
				protobufs.AgentCapabilities_AgentCapabilities_ReportsCommandStatus,
			commandErr: errors.New("restart failed"),
			duringStatus: &protobufs.CommandStatus{
				CommandId: commandID,
				Type:      protobufs.CommandType_CommandType_Restart,
				Status:    protobufs.CommandStatuses_CommandStatuses_EXECUTING,
			},
			afterStatus: &protobufs.CommandStatus{
				CommandId:    commandID,
				Type:         protobufs.CommandType_CommandType_Restart,
				Status:       protobufs.CommandStatuses_CommandStatuses_FAILED,
				ErrorMessage: "restart failed",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clientSyncedState := &ClientSyncedState{}
			require.NoError(t, clientSyncedState.SetCapabilities(&test.capabilities))
			sender := NewSender(&sharedinternal.NopLogger{})

			var duringStatus *protobufs.CommandStatus
			callbacks := types.Callbacks{
				OnCommand: func(_ context.Context, _ *protobufs.ServerToAgentCommand) error {
					duringStatus = clientSyncedState.CommandStatus()
					return test.commandErr
				},
			}
			callbacks.SetDefaults()

			processor := newReceivedProcessor(&sharedinternal.NopLogger{}, callbacks, sender, clientSyncedState, nil, new(sync.Mutex), time.Second)
			processor.ProcessReceivedMessage(context.Background(), &protobufs.ServerToAgent{
				Command: &protobufs.ServerToAgentCommand{
					Type:      protobufs.CommandType_CommandType_Restart,
					CommandId: commandID,
				},
			})

			assertCommandStatus(t, test.duringStatus, duringStatus)
			assertCommandStatus(t, test.afterStatus, clientSyncedState.CommandStatus())
			assertCommandStatus(t, test.afterStatus, sender.NextMessage().PopPending().GetCommandStatus())
		})
	}
}

func assertCommandStatus(t *testing.T, expected, actual *protobufs.CommandStatus) {
	t.Helper()
	if expected == nil {
		assert.Nil(t, actual)
		return
	}
	require.NotNil(t, actual)
	assert.Equal(t, expected.CommandId, actual.CommandId)
	assert.Equal(t, expected.Type, actual.Type)
	assert.Equal(t, expected.Status, actual.Status)
	assert.Equal(t, expected.ErrorMessage, actual.ErrorMessage)
}
