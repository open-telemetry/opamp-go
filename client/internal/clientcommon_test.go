package internal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// nopSender implements the Sender interface
type nopSender struct{}

func (n nopSender) NextMessage() *NextMessage {
	return nil
}
func (n nopSender) ScheduleSend() {}
func (n nopSender) SetInstanceUid(_ types.InstanceUid) error {
	return nil
}

func (n nopSender) SetHeartbeatInterval(_ time.Duration) error {
	return nil
}

func TestPrepareStart(t *testing.T) {
	tests := []struct {
		name     string
		settings types.StartSettings
		err      error
	}{{
		name:     "ok",
		settings: types.StartSettings{},
		err:      nil,
	}, {
		name: "request connection settings request ok",
		settings: types.StartSettings{
			Capabilities:              protobufs.AgentCapabilities_AgentCapabilities_AcceptsOpAMPConnectionSettings,
			RequestConnectionSettings: true,
		},
		err: nil,
	}, {
		name: "request connection settings without capability",
		settings: types.StartSettings{
			RequestConnectionSettings: true,
		},
		err: ErrAcceptsOpAMPConnectionsSettingsNotSet,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &ClientCommon{
				ClientSyncedState: ClientSyncedState{
					agentDescription: &protobufs.AgentDescription{},
				},
				sender: nopSender{},
			}
			err := client.PrepareStart(context.Background(), tt.settings)
			assert.ErrorIs(t, err, tt.err)
		})
	}
}
