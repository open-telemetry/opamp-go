package internal

import (
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
)

// MockSender is a mock implementation of the Sender interface for testing purposes.
type MockSender struct {
	nextMessage *NextMessage
}

// NewMockSender creates a new MockSender with default values.
func NewMockSender() *MockSender {
	nm := NewNextMessage()
	return &MockSender{
		nextMessage: &nm,
	}
}

// NextMessage returns the next message to be sent.
func (m *MockSender) NextMessage() *NextMessage {
	return m.nextMessage
}

// ScheduleSend signals that a message is ready to be sent.
func (m *MockSender) ScheduleSend() {}

// SetInstanceUid sets the instance UID for subsequent messages.
func (m *MockSender) SetInstanceUid(instanceUid types.InstanceUid) error {
	return nil
}

// SetHeartbeatInterval sets the heartbeat interval.
func (m *MockSender) SetHeartbeatInterval(duration time.Duration) error {
	return nil
}
