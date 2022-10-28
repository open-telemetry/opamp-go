package internal

import (
	"errors"

	"github.com/oklog/ulid/v2"
	"github.com/open-telemetry/opamp-go/protobufs"
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

	// SetInstanceUid sets a new instanceUid to be used for all subsequent messages to be sent.
	SetInstanceUid(instanceUid string) error
}

// SenderCommon is partial Sender implementation that is common between WebSocket and plain
// HTTP transports. This struct is intended to be embedded in the WebSocket and
// HTTP Sender implementations.
type SenderCommon struct {
	// Indicates that there is a pending message to send.
	hasPendingMessage chan struct{}

	onMessageDone chan struct{}

	// Indicates onMessage occurring
	delayScheduleSend bool

	// The next message to send.
	nextMessage NextMessage
}

// NewSenderCommon creates a new SenderCommon. This is intended to be used by
// the WebSocket and HTTP Sender implementations.
func NewSenderCommon() SenderCommon {
	return SenderCommon{
		hasPendingMessage: make(chan struct{}, 1),
		onMessageDone:     make(chan struct{}, 1),
		nextMessage:       NewNextMessage(),
		delayScheduleSend: false,
	}
}

// ScheduleSend signals to HTTPSender that the message in NextMessage struct
// is now ready to be sent. If there is no pending message (e.g. the NextMessage was
// already sent and "pending" flag is reset) then no message will be sent.
func (h *SenderCommon) ScheduleSend() {
	if h.delayScheduleSend {
		go func() {
			<-h.onMessageDone
			h.ScheduleSend()
		}()
		return
	}
	select {
	// Set pending flag. Don't block on writing to channel.
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

// DisableScheduleSend stops ScheduleSend() from adding new messages.
// It is used whenever OnMessage callback is taking place
func (h *SenderCommon) DisableScheduleSend() {
	h.delayScheduleSend = true
}

// EnableScheduleSend enables ScheduleSend() to add new messages.
// It is used after OnMessage callback is done processing a msg
func (h *SenderCommon) EnableScheduleSend() {
	h.delayScheduleSend = false
	select {
	case h.onMessageDone <- struct{}{}:
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
