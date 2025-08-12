package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestClientSyncedStateConnectionSettingsStatus(t *testing.T) {
	state := &ClientSyncedState{}

	// starts nil
	status := state.ConnectionSettingsStatus()
	assert.Nil(t, status)

	// attempting to set as nil returns error
	err := state.SetConnectionSettingsStatus(nil)
	assert.ErrorIs(t, errConnectionSettingsStatusMissing, err)
	status = state.ConnectionSettingsStatus()
	assert.Nil(t, status)

	// state can be stored
	err = state.SetConnectionSettingsStatus(&protobufs.ConnectionSettingsStatus{
		Status: protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_APPLIED,
	})
	assert.NoError(t, err)
	status = state.ConnectionSettingsStatus()
	assert.NotNil(t, status)
	assert.Equal(t, protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_APPLIED, status.Status)
}
