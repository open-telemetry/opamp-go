package internal

import (
	"context"
	"errors"
	"fmt"
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
		// Defaults to Restart.
		commandType protobufs.CommandType
		commandErr  error
		// Whether OnCommand is expected to be called.
		notCalled bool
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
		{
			name:         "restart not accepted",
			capabilities: protobufs.AgentCapabilities_AgentCapabilities_ReportsCommandStatus,
			notCalled:    true,
			afterStatus: &protobufs.CommandStatus{
				CommandId:    commandID,
				Type:         protobufs.CommandType_CommandType_Restart,
				Status:       protobufs.CommandStatuses_CommandStatuses_UNSUPPORTED,
				ErrorMessage: "command CommandType_Restart is not accepted by the agent",
			},
		},
		{
			name: "agent does not support the command type",
			capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand |
				protobufs.AgentCapabilities_AgentCapabilities_ReportsCommandStatus,
			commandType: protobufs.CommandType(42),
			commandErr:  fmt.Errorf("unknown command: %w", types.ErrCommandUnsupported),
			duringStatus: &protobufs.CommandStatus{
				CommandId: commandID,
				Type:      protobufs.CommandType(42),
				Status:    protobufs.CommandStatuses_CommandStatuses_EXECUTING,
			},
			afterStatus: &protobufs.CommandStatus{
				CommandId:    commandID,
				Type:         protobufs.CommandType(42),
				Status:       protobufs.CommandStatuses_CommandStatuses_UNSUPPORTED,
				ErrorMessage: "unknown command: command is not supported",
			},
		},
		{
			name:      "restart not accepted without status capability",
			notCalled: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clientSyncedState := &ClientSyncedState{}
			require.NoError(t, clientSyncedState.SetCapabilities(&test.capabilities))
			sender := NewSender(&sharedinternal.NopLogger{})

			var duringStatus *protobufs.CommandStatus
			called := false
			callbacks := types.Callbacks{
				OnCommand: func(_ context.Context, _ *protobufs.ServerToAgentCommand) error {
					called = true
					duringStatus = clientSyncedState.CommandStatus()
					return test.commandErr
				},
			}
			callbacks.SetDefaults()

			processor := newReceivedProcessor(&sharedinternal.NopLogger{}, callbacks, sender, clientSyncedState, nil, new(sync.Mutex), time.Second)
			processor.ProcessReceivedMessage(t.Context(), &protobufs.ServerToAgent{
				Command: &protobufs.ServerToAgentCommand{
					Type:      test.commandType,
					CommandId: commandID,
				},
			})

			assert.Equal(t, !test.notCalled, called)

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
