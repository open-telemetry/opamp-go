package internal

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

type TestLogger struct {
	*testing.T
}

func (logger TestLogger) Debugf(format string, v ...interface{}) {
	logger.Logf(format, v...)
}

type commandAction int

const (
	none commandAction = iota
	restart
	unknown
)

func TestServerToAgentCommand(t *testing.T) {

	tests := []struct {
		command *protobufs.ServerToAgentCommand
		action  commandAction
		message string
	}{
		{
			command: nil,
			action:  none,
			message: "No command should result in no action",
		},
		{
			command: &protobufs.ServerToAgentCommand{
				Type: protobufs.CommandType_CommandType_Restart,
			},
			action:  restart,
			message: "A Restart command should result in a restart",
		},
		{
			command: &protobufs.ServerToAgentCommand{
				Type: -1,
			},
			action:  unknown,
			message: "An unknown command is still passed to the OnCommand callback",
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			action := none

			callbacks := types.CallbacksStruct{
				OnCommandFunc: func(command *protobufs.ServerToAgentCommand) error {
					switch command.Type {
					case protobufs.CommandType_CommandType_Restart:
						action = restart
					default:
						action = unknown
					}
					return nil
				},
			}
			clientSyncedState := ClientSyncedState{
				remoteConfigStatus: &protobufs.RemoteConfigStatus{},
			}
			sender := WSSender{}
			receiver := NewWSReceiver(TestLogger{t}, NewCallbacksWrapper(callbacks), nil, &sender, &clientSyncedState, nil, 0)
			receiver.processor.ProcessReceivedMessage(context.Background(), &protobufs.ServerToAgent{
				Command: test.command,
			})
			assert.Equal(t, test.action, action, test.message)
		})
	}
}

func TestServerToAgentCommandExclusive(t *testing.T) {
	calledCommand := false
	calledOnMessageConfig := false

	callbacks := types.CallbacksStruct{
		OnCommandFunc: func(command *protobufs.ServerToAgentCommand) error {
			calledCommand = true
			return nil
		},
		OnMessageFunc: func(ctx context.Context, msg *types.MessageData) {
			calledOnMessageConfig = true
		},
	}
	clientSyncedState := ClientSyncedState{}
	receiver := NewWSReceiver(TestLogger{t}, NewCallbacksWrapper(callbacks), nil, nil, &clientSyncedState, nil, 0)
	receiver.processor.ProcessReceivedMessage(context.Background(), &protobufs.ServerToAgent{
		Command: &protobufs.ServerToAgentCommand{
			Type: protobufs.CommandType_CommandType_Restart,
		},
		RemoteConfig: &protobufs.AgentRemoteConfig{},
	})
	assert.Equal(t, true, calledCommand, "OnCommand should be called when a Command is specified")
	assert.Equal(t, false, calledOnMessageConfig, "OnMessage should not be called when a Command is specified")
}

func TestDecodeMessage(t *testing.T) {
	msgsToTest := []*protobufs.ServerToAgent{
		{}, // Empty message
		{
			InstanceUid: "abcd",
		},
	}

	// Try with and without header byte. This is only necessary until the
	// end of grace period that ends Feb 1, 2023. After that the header is
	// no longer optional.
	withHeaderTests := []bool{false, true}

	for _, msg := range msgsToTest {
		for _, withHeader := range withHeaderTests {
			bytes, err := proto.Marshal(msg)
			require.NoError(t, err)

			if withHeader {
				// Prepend zero header byte.
				bytes = append([]byte{0}, bytes...)
			}

			var decoded protobufs.ServerToAgent
			err = internal.DecodeWSMessage(bytes, &decoded)
			require.NoError(t, err)

			assert.True(t, proto.Equal(msg, &decoded))
		}
	}
}
