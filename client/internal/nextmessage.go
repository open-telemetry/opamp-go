package internal

import (
	"sync"

	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

// nextMessage encapsulates the next message to be sent and provides a
// concurrency-safe interface to work with the message.
type nextMessage struct {
	// The next message to send.
	nextMessage protobufs.AgentToServer
	// Indicates that nextMessage is pending to be sent.
	messagePending bool
	// Mutex to protect the above 2 fields.
	messageMutex sync.Mutex
}

// Update applies the specified modifier function to the next message that
// will be sent and marks the message as pending to be sent.
func (s *nextMessage) Update(modifier func(msg *protobufs.AgentToServer)) {
	s.messageMutex.Lock()
	modifier(&s.nextMessage)
	s.messagePending = true
	s.messageMutex.Unlock()
}

// UpdateStatus applies the specified modifier function to the status report that
// will be sent next and marks the status report as pending to be sent.
func (s *nextMessage) UpdateStatus(modifier func(statusReport *protobufs.StatusReport)) {
	s.Update(
		func(msg *protobufs.AgentToServer) {
			if s.nextMessage.StatusReport == nil {
				s.nextMessage.StatusReport = &protobufs.StatusReport{}
			}
			modifier(s.nextMessage.StatusReport)
		},
	)
}

// PopPending returns the next message to be sent, if it is pending or nil otherwise.
// Clears the "pending" flag.
func (s *nextMessage) PopPending() *protobufs.AgentToServer {
	var msgToSend *protobufs.AgentToServer
	s.messageMutex.Lock()
	if s.messagePending {
		// Clone the message to have a copy for sending and avoid blocking
		// future updates to s.nextMessage field.
		msgToSend = proto.Clone(&s.nextMessage).(*protobufs.AgentToServer)
		s.messagePending = false

		// Reset fields that we do not have to send unless they change before the
		// next report after this one.
		s.nextMessage = protobufs.AgentToServer{}
	}
	s.messageMutex.Unlock()
	return msgToSend
}
