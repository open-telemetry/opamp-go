package internal

import (
	"context"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testLogger struct {
	testing.TB
}

func (t *testLogger) Debugf(_ context.Context, format string, v ...any) {
	t.Logf("DEBUG: "+format, v...)
}

func (t *testLogger) Errorf(_ context.Context, format string, v ...any) {
	t.Logf("ERROR: "+format, v...)
}

func TestPrepareStart(t *testing.T) {
	uid := types.InstanceUid{0xd, 0xe, 0xa, 0xd, 0xb, 0xe, 0xe, 0xf, 0xd, 0xe, 0xa, 0xd, 0xb, 0xe, 0xe, 0xf}

	t.Run("client already started", func(t *testing.T) {
		c := ClientCommon{isStarted: true}
		err := c.PrepareStart(t.Context(), types.StartSettings{})
		assert.ErrorIs(t, err, errAlreadyStarted)
	})

	t.Run("description not set", func(t *testing.T) {
		c := NewClientCommon(&testLogger{t}, nil)
		err := c.PrepareStart(t.Context(), types.StartSettings{})
		assert.ErrorIs(t, err, ErrAgentDescriptionMissing)
	})

	t.Run("only reportsstatus capability", func(t *testing.T) {
		sender := NewMockSender()
		c := NewClientCommon(&testLogger{t}, sender)

		capabilities := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus
		err := c.SetCapabilities(&capabilities)
		require.NoError(t, err)

		description := &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "test-agent"},
				},
			}},
		}
		err = c.SetAgentDescription(description)
		require.NoError(t, err)

		err = c.PrepareStart(t.Context(), types.StartSettings{InstanceUid: uid})
		require.NoError(t, err)

		assert.Nil(t, c.PackagesStateProvider, "Expected PackagesStateProvider to be nil")
		assert.Zero(t, c.DownloadReporterInterval)
		assert.Nil(t, c.ClientSyncedState.Health(), "expected state health to be nil")
		assert.Nil(t, c.ClientSyncedState.RemoteConfigStatus(), "expected state remote config status to be nil")
		assert.Nil(t, c.ClientSyncedState.ConnectionSettingsStatus(), "expected state connection settings status to be nil")
		assert.Nil(t, c.ClientSyncedState.PackageStatuses(), "expected state package statuses to be nil")
		assert.Nil(t, c.ClientSyncedState.CustomCapabilities(), "expected state custom capabilities to be nil")
		assert.Nil(t, c.ClientSyncedState.AvailableComponents(), "expected state available components to be nil")
		assert.Zero(t, c.ClientSyncedState.Flags(), "expected state flags to be zero")
		assert.Equal(t, capabilities, c.ClientSyncedState.Capabilities())
	})

	// Currently not setting capabilities should result in the similar behaviour as only setting the reports status capability
	t.Run("no capabilities set", func(t *testing.T) {
		sender := NewMockSender()
		c := NewClientCommon(&testLogger{t}, sender)

		description := &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "test-agent"},
				},
			}},
		}
		err := c.SetAgentDescription(description)
		require.NoError(t, err)

		err = c.PrepareStart(t.Context(), types.StartSettings{InstanceUid: uid})
		require.NoError(t, err)

		assert.Nil(t, c.PackagesStateProvider, "Expected PackagesStateProvider to be nil")
		assert.Zero(t, c.DownloadReporterInterval)
		assert.Nil(t, c.ClientSyncedState.Health(), "expected state health to be nil")
		assert.Nil(t, c.ClientSyncedState.RemoteConfigStatus(), "expected state remote config status to be nil")
		assert.Nil(t, c.ClientSyncedState.ConnectionSettingsStatus(), "expected state connection settings status to be nil")
		assert.Nil(t, c.ClientSyncedState.PackageStatuses(), "expected state package statuses to be nil")
		assert.Nil(t, c.ClientSyncedState.CustomCapabilities(), "expected state custom capabilities to be nil")
		assert.Nil(t, c.ClientSyncedState.AvailableComponents(), "expected state available components to be nil")
		assert.Zero(t, c.ClientSyncedState.Flags(), "expected state flags to be zero")
		assert.Equal(t, protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus, c.ClientSyncedState.Capabilities())
	})

	t.Run("all capabilities set", func(t *testing.T) {
		sender := NewMockSender()
		c := NewClientCommon(&testLogger{t}, sender)

		err := c.SetHealth(&protobufs.ComponentHealth{Healthy: true})
		require.NoError(t, err)

		err = c.SetAvailableComponents(&protobufs.AvailableComponents{
			Hash: []byte(`testhash`),
		})
		require.NoError(t, err)

		capabilities := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus | protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig | protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig | protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages | protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses | protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnTraces | protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics | protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnLogs | protobufs.AgentCapabilities_AgentCapabilities_AcceptsOpAMPConnectionSettings | protobufs.AgentCapabilities_AgentCapabilities_AcceptsOtherConnectionSettings | protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand | protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth | protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig | protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat | protobufs.AgentCapabilities_AgentCapabilities_ReportsAvailableComponents | protobufs.AgentCapabilities_AgentCapabilities_ReportsConnectionSettingsStatus
		err = c.SetCapabilities(&capabilities)
		require.NoError(t, err)

		description := &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "test-agent"},
				},
			}},
		}
		err = c.SetAgentDescription(description)
		require.NoError(t, err)

		interval := 5 * time.Second

		err = c.PrepareStart(t.Context(), types.StartSettings{
			InstanceUid:           uid,
			PackagesStateProvider: NewInMemPackagesStore(),
			// use Unset remote config status
			HeartbeatInterval:        &interval,
			DownloadReporterInterval: &interval,
		})
		require.NoError(t, err)

		assert.NotNil(t, c.PackagesStateProvider, "expected PackagesStateProvider to not be nil")
		assert.Equal(t, interval, c.DownloadReporterInterval)
		if assert.NotNil(t, c.ClientSyncedState.Health(), "expected state health to not be nil") {
			assert.True(t, c.ClientSyncedState.Health().Healthy, "expected client to be healthy")
		}
		if assert.NotNil(t, c.ClientSyncedState.RemoteConfigStatus(), "expected state remote config status to not be nil") {
			assert.Equal(t, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_UNSET, c.ClientSyncedState.RemoteConfigStatus().Status)
		}
		if assert.NotNil(t, c.ClientSyncedState.PackageStatuses(), "expected state package statuses to not be nil") {
			assert.Empty(t, c.ClientSyncedState.PackageStatuses().ServerProvidedAllPackagesHash, "expected package statuses to have not set hash")
		}
		if assert.NotNil(t, c.ClientSyncedState.AvailableComponents(), "expected state available components to not be nil") {
			assert.EqualValues(t, []byte(`testhash`), c.ClientSyncedState.AvailableComponents().Hash, "Expected hashes to be equal")
		}
		assert.Nil(t, c.ClientSyncedState.ConnectionSettingsStatus(), "expected state connection settings status to be nil")
		assert.Nil(t, c.ClientSyncedState.CustomCapabilities(), "expected state custom capabilities to be nil")
		assert.Zero(t, c.ClientSyncedState.Flags(), "expected state flags to be zero")
		assert.Equal(t, capabilities, c.ClientSyncedState.Capabilities())
	})
}
