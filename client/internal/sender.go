package internal

import (
	"errors"
	"github.com/oklog/ulid/v2"
	"github.com/open-telemetry/opamp-go/protobufs"
	"sync/atomic"
	"time"
)

// Sender is an interface of the sending portion of OpAMP protocol that stores
// the NextMessage to be sent and can be ordered to send the message.
type Sender interface {
	// NextMessage gives access to the next message that will be sent by this Sender.
	// Can be called concurrently with any other method.
	NextMessage() *NextMessage

	// ScheduleSend signals to Sender that the message in NextMessage struct
	// is now ready to be sent.  The Sender should send the NextMessage as soon as possible.
	// If there is no pending message (e.g. the NextMessage was already sent and
	// "pending" flag is reset) then no message will be sent.
	ScheduleSend()

	// DisableScheduleSend temporary preventing ScheduleSend from writing to channel
	DisableScheduleSend()

	// EnableScheduleSend re-enables ScheduleSend and checks if it was called during onMessage callback
	EnableScheduleSend()

	// SetInstanceUid sets a new instanceUid to be used for all subsequent messages to be sent.
	SetInstanceUid(instanceUid string) error
}

// SenderCommon is partial Sender implementation that is common between WebSocket and plain
// HTTP transports. This struct is intended to be embedded in the WebSocket and
// HTTP Sender implementations.
type SenderCommon struct {
	// Indicates that there is a pending message to send.
	hasPendingMessage chan struct{}

	// When set to non-zero indicates message sending is disabled
	isSendingDisabled int32

	// Indicates ScheduleSend() was called when message sending was disabled
	registerScheduleSend chan struct{}

	// The next message to send.
	nextMessage NextMessage
}

// NewSenderCommon creates a new SenderCommon. This is intended to be used by
// the WebSocket and HTTP Sender implementations.
func NewSenderCommon() SenderCommon {
	return SenderCommon{
		hasPendingMessage:    make(chan struct{}, 1),
		registerScheduleSend: make(chan struct{}, 1),
		nextMessage:          NewNextMessage(),
		isSendingDisabled:    0,
	}
}

// ScheduleSend signals to HTTPSender that the message in NextMessage struct
// is now ready to be sent. If there is no pending message (e.g. the NextMessage was
// already sent and "pending" flag is reset) then no message will be sent.
func (h *SenderCommon) ScheduleSend() {
	if h.IsSendingDisabled() {
		// Register message sending to when message sending is enabled, won't block on writing to channel.
		select {
		case h.registerScheduleSend <- struct{}{}:
		default:
			break
		}
		return
	}

	// Set pending flag. Don't block on writing to channel.
	select {
	case h.hasPendingMessage <- struct{}{}:
	default:
		break
	}
}

// NextMessage gives access to the next message that will be sent by this looper.
// Can be called concurrently with any other method.
func (h *SenderCommon) NextMessage() *NextMessage {
	return &h.nextMessage
}

// IsSendingDisabled returns true when onMessage callback is running
func (h *SenderCommon) IsSendingDisabled() bool {
	return atomic.LoadInt32(&h.isSendingDisabled) != 0
}

// DisableScheduleSend temporary preventing ScheduleSend from writing to channel
func (h *SenderCommon) DisableScheduleSend() {

	atomic.StoreInt32(&h.isSendingDisabled, 1)
}

// EnableScheduleSend re-enables message sending, won't block on reading from channel.
func (h *SenderCommon) EnableScheduleSend() {
	atomic.StoreInt32(&h.isSendingDisabled, 0)
	select {
	case <-h.registerScheduleSend:
		h.ScheduleSend()
	case <-time.Tick(100 * time.Millisecond):
		break
	default:
		break
	}
}

// SetInstanceUid sets a new instanceUid to be used for all subsequent messages to be sent.
// Can be called concurrently, normally is called when a message is received from the
// Server that instructs us to change our instance UID.
func (h *SenderCommon) SetInstanceUid(instanceUid string) error {
	if instanceUid == "" {
		return errors.New("cannot set instance uid to empty value")
	}

	if _, err := ulid.ParseStrict(instanceUid); err != nil {
		return err
	}

	h.nextMessage.Update(
		func(msg *protobufs.AgentToServer) {
			msg.InstanceUid = instanceUid
		})

	return nil
}
