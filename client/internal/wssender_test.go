package internal

import (
	"testing"
	"time"

	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/stretchr/testify/assert"
)

func TestWSSenderSetHeartbeatInterval(t *testing.T) {
	sender := NewSender(&sharedinternal.NopLogger{})

	// Default interval should be 30s as per OpAMP Specification
	assert.Equal(t, int64((30 * time.Second).Seconds()), sender.heartbeatIntervalSeconds.Load())

	// negative interval is invalid for http sender
	assert.Error(t, sender.SetHeartbeatInterval(-1))
	assert.Equal(t, int64((30 * time.Second).Seconds()), sender.heartbeatIntervalSeconds.Load())

	// zero is valid for ws sender
	assert.NoError(t, sender.SetHeartbeatInterval(0))
	assert.Equal(t, int64(0), sender.heartbeatIntervalSeconds.Load())

	var expected int64 = 10
	assert.NoError(t, sender.SetHeartbeatInterval(time.Duration(expected)*time.Second))
	assert.Equal(t, expected, sender.heartbeatIntervalSeconds.Load())
}
