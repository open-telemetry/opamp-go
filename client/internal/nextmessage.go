package internal

import (
	"sync"

	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

// NextMessage encapsulates the next message to be sent and provides a
// concurrency-safe interface to work with the message.
type NextMessage struct {
	// The next message to send.
	nextMessage *protobufs.AgentToServer
	// Indicates that nextMessage is pending to be sent.
	messagePending bool
	// Mutex to protect the above 2 fields.
	messageMutex sync.Mutex
}

func NewNextMessage() NextMessage {
	return NextMessage{
		nextMessage: &protobufs.AgentToServer{},
	}
}

// Update applies the specified modifier function to the next message that
// will be sent and marks the message as pending to be sent.
func (s *NextMessage) Update(modifier func(msg *protobufs.AgentToServer)) {
	s.messageMutex.Lock()
	modifier(s.nextMessage)
	s.messagePending = true
	s.messageMutex.Unlock()
}

// PopPending returns the next message to be sent, if it is pending or nil otherwise.
// Clears the "pending" flag.
func (s *NextMessage) PopPending() *protobufs.AgentToServer {
	var msgToSend *protobufs.AgentToServer
	s.messageMutex.Lock()
	if s.messagePending {
		// Clone the message to have a copy for sending and avoid blocking
		// future updates to s.NextMessage field.
		msgToSend = proto.Clone(s.nextMessage).(*protobufs.AgentToServer)
		s.messagePending = false

		// Reset fields that we do not have to send unless they change before the
		// next report after this one.
		msg := &protobufs.AgentToServer{
			InstanceUid: s.nextMessage.InstanceUid,
			// Increment the sequence number.
			SequenceNum:  s.nextMessage.SequenceNum + 1,
			Capabilities: s.nextMessage.Capabilities,
		}

		s.nextMessage = msg
	}
	s.messageMutex.Unlock()
	return msgToSend
}
