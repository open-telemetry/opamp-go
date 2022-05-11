package internal

import (
	"context"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// WSSender implements the WebSocket client's sending portion of OpAMP protocol.
type WSSender struct {
	SenderCommon
	conn   *websocket.Conn
	logger types.Logger
	// Indicates that the sender has fully stopped.
	stopped chan struct{}
}

func NewSender(logger types.Logger) *WSSender {
	return &WSSender{
		logger:       logger,
		SenderCommon: NewSenderCommon(),
	}
}

// Start the sender and send the first message that was set via NextMessage().Update()
// earlier. To stop the WSSender cancel the ctx.
func (s *WSSender) Start(ctx context.Context, conn *websocket.Conn) error {
	s.conn = conn
	err := s.sendNextMessage()

	// Run the sender in the background.
	s.stopped = make(chan struct{})
	go s.run(ctx)

	return err
}

// WaitToStop blocks until the sender is stopped. To stop the sender cancel the context
// that was passed to Start().
func (s *WSSender) WaitToStop() {
	<-s.stopped
}

func (s *WSSender) run(ctx context.Context) {
out:
	for {
		select {
		case <-s.hasPendingMessage:
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
