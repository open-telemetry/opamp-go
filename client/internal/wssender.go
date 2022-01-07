package internal

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// WSSender implements the WebSocket client's sending portion of OpAMP protocol.
type WSSender struct {
	instanceUid atomic.Value
	conn        *websocket.Conn

	logger types.Logger

	// Indicates that there are pending messages to send.
	hasMessages chan struct{}

	// The next message to send.
	nextMessage nextMessage

	// Indicates that the sender has fully stopped.
	stopped chan struct{}
}

func NewSender(logger types.Logger) *WSSender {
	return &WSSender{
		logger:      logger,
		hasMessages: make(chan struct{}, 1),
	}
}

// Start the sender and send the first message that was set via NextMessage().Update()
// earlier. To stop the WSSender cancel the ctx.
func (s *WSSender) Start(ctx context.Context, instanceUid string, conn *websocket.Conn) error {
	s.conn = conn
	s.instanceUid.Store(instanceUid)
	err := s.sendNextMessage()

	// Run the sender in the background.
	s.stopped = make(chan struct{})
	go s.run(ctx)

	return err
}

// SetInstanceUid sets a new instanceUid without closing and reopening the connection. It will be used
// when next message is being sent.
func (s *WSSender) SetInstanceUid(instanceUid string) error {
	if instanceUid == "" {
		return fmt.Errorf("cannot set instance uid to empty value")
	}
	s.instanceUid.Store(instanceUid)
	return nil
}

func (s *WSSender) NextMessage() *nextMessage {
	return &s.nextMessage
}

// WaitToStop blocks until the sender is stopped. To stop the sender cancel the context
// that was passed to Start().
func (s *WSSender) WaitToStop() {
	<-s.stopped
}

// ScheduleSend signals to the sending goroutine to send the next message
// if it is pending. If there is no pending message (e.g. the message was
// already sent and "pending" flag is reset) then no message will be be sent.
func (s *WSSender) ScheduleSend() {
	select {
	case s.hasMessages <- struct{}{}:
	default:
		break
	}
}

func (s *WSSender) run(ctx context.Context) {
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

func (s *WSSender) sendNextMessage() error {
	msgToSend := s.nextMessage.PopPending()
	if msgToSend != nil && !proto.Equal(msgToSend, &protobufs.AgentToServer{}) {
		// There is a pending message and the message has some fields populated.
		// Set the InstanceUid field and send it.
		msgToSend.InstanceUid = s.instanceUid.Load().(string)
		return s.sendMessage(msgToSend)
	}
	return nil
}

func (s *WSSender) sendMessage(msg *protobufs.AgentToServer) error {
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
