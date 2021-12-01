package internal

import (
	"context"
	"sync"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/protobufs"
)

// Sender implements the client's sending portion of OpAMP protocol.
type Sender struct {
	instanceUid string
	conn        *websocket.Conn

	logger types.Logger

	// Indicates that there are pending messages to send.
	hasMessages chan struct{}

	// The next message to send.
	nextMessage protobufs.AgentToServer
	// Indicates that nextMessage is pending to be sent.
	messagePending bool
	// Mutex to protect the above 2 fields.
	messageMutex sync.Mutex

	// Indicates that the sender has fully stopped.
	stopped chan struct{}
}

func NewSender(logger types.Logger) *Sender {
	return &Sender{
		logger:      logger,
		hasMessages: make(chan struct{}, 1),
	}
}

// Start the sender and send the first message that was set via UpdateNextMessage()
// earlier. To stop the Sender cancel the ctx.
func (s *Sender) Start(ctx context.Context, instanceUid string, conn *websocket.Conn) error {
	s.conn = conn
	s.instanceUid = instanceUid
	err := s.sendNextMessage()

	// Run the sender in the background.
	s.stopped = make(chan struct{})
	go s.run(ctx)

	return err
}

// WaitToStop blocks until the sender is stopped. To stop the sender cancel the context
// that was passed to Start().
func (s *Sender) WaitToStop() {
	<-s.stopped
}

// UpdateNextMessage applies the specified modifier function to the next message that
// will be sent and marks the message as pending to be sent.
func (s *Sender) UpdateNextMessage(modifier func(msg *protobufs.AgentToServer)) {
	s.messageMutex.Lock()
	modifier(&s.nextMessage)
	s.messagePending = true
	s.messageMutex.Unlock()
}

// UpdateNextStatus applies the specified modifier function to the status report that
// will be sent next and marks the status report as pending to be sent.
func (s *Sender) UpdateNextStatus(modifier func(statusReport *protobufs.StatusReport)) {
	s.UpdateNextMessage(
		func(msg *protobufs.AgentToServer) {
			if s.nextMessage.StatusReport == nil {
				s.nextMessage.StatusReport = &protobufs.StatusReport{}
			}
			modifier(s.nextMessage.StatusReport)
		},
	)
}

// ScheduleSend signals to the sending goroutine to send the next message
// if it is pending. If there is no pending message (e.g. the message was
// already sent and "pending" flag is reset) then no message will be be sent.
func (s *Sender) ScheduleSend() {
	select {
	case s.hasMessages <- struct{}{}:
	default:
		break
	}
}

func (s *Sender) run(ctx context.Context) {
out:
	for {
		select {
		case <-s.hasMessages:
			s.sendNextMessage()

		case <-ctx.Done():
			break out
		}
	}

	close(s.stopped)
}

func (s *Sender) sendNextMessage() error {
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

	if msgToSend != nil && !proto.Equal(msgToSend, &protobufs.AgentToServer{}) {
		// There is a pending message and the message has some fields populated.
		// Set the InstanceUid field and send it.
		msgToSend.InstanceUid = s.instanceUid
		return s.sendMessage(msgToSend)
	}
	return nil
}

func (s *Sender) sendMessage(msg *protobufs.AgentToServer) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		s.logger.Errorf("Cannot marshal data: %v", err)
		return err
	}
	err = s.conn.WriteMessage(websocket.BinaryMessage, data)
	if err != nil {
		s.logger.Errorf("Cannot send: %v", err)
		// TODO: propagate error back to Client and reconnect.
	}
	return err
}
