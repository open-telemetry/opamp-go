package internal

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/open-telemetry/opamp-go/client/types"
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
				Type: protobufs.ServerToAgentCommand_Restart,
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
					case protobufs.ServerToAgentCommand_Restart:
						action = restart
					default:
						action = unknown
					}
					return nil
				},
			}
			receiver := NewReceiver(TestLogger{t}, callbacks, nil, nil)
			receiver.processReceivedMessage(context.Background(), &protobufs.ServerToAgent{
				Command: test.command,
			})
			assert.Equal(t, test.action, action, test.message)
		})
	}
}

func TestServerToAgentCommandExclusive(t *testing.T) {
	calledCommand := false
	calledRemoteConfig := false

	callbacks := types.CallbacksStruct{
		OnCommandFunc: func(command *protobufs.ServerToAgentCommand) error {
			calledCommand = true
			return nil
		},
		OnRemoteConfigFunc: func(ctx context.Context, remoteConfig *protobufs.AgentRemoteConfig) (effectiveConfig *protobufs.EffectiveConfig, configChanged bool, err error) {
			calledRemoteConfig = true
			return nil, false, nil
		},
	}
	receiver := NewReceiver(TestLogger{t}, callbacks, nil, nil)
	receiver.processReceivedMessage(context.Background(), &protobufs.ServerToAgent{
		Command: &protobufs.ServerToAgentCommand{
			Type: protobufs.ServerToAgentCommand_Restart,
		},
		RemoteConfig: &protobufs.AgentRemoteConfig{},
	})
	assert.Equal(t, true, calledCommand, "OnCommand should be called when a Command is specified")
	assert.Equal(t, false, calledRemoteConfig, "OnRemoteConfig should not be called when a Command is specified")
}

func TestOnRemoteConfigReportsError(t *testing.T) {
	expectMsg := "error occurred while handling configuration"
	expectStatus := protobufs.RemoteConfigStatus_Status(protobufs.RemoteConfigStatus_Failed)

	callbacks := types.CallbacksStruct{
		OnRemoteConfigFunc: func(ctx context.Context, remoteConfig *protobufs.AgentRemoteConfig) (effectiveConfig *protobufs.EffectiveConfig, configChanged bool, err error) {
			return nil, false, errors.New(expectMsg)
		},
	}

	sender := NewSender(TestLogger{t})
	receiver := NewReceiver(TestLogger{t}, callbacks, nil, sender)

	receiver.processReceivedMessage(context.Background(), &protobufs.ServerToAgent{
		RemoteConfig: &protobufs.AgentRemoteConfig{},
	})

	gotStatus := receiver.sender.nextMessage.StatusReport.RemoteConfigStatus.Status
	gotMsg := receiver.sender.nextMessage.StatusReport.RemoteConfigStatus.ErrorMessage

	assert.Equal(t, expectStatus, gotStatus)
	assert.Equal(t, expectMsg, gotMsg)
}
