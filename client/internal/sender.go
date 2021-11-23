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

	// The next status report to send.
	statusReport protobufs.StatusReport
	// Indicates that the status report is pending to be sent.
	statusReportPending bool
	// Mutex to protect the above 2 fields.
	statusReportMutex sync.Mutex

	// Indicates that the sender has fully stopped.
	stopped chan struct{}
}

func NewSender(logger types.Logger) *Sender {
	return &Sender{
		logger:      logger,
		hasMessages: make(chan struct{}, 1),
	}
}

// Start the sender and send the first status report that was set via UpdateStatus()
// earlier. To stop the Sender cancel the ctx.
func (s *Sender) Start(ctx context.Context, instanceUid string, conn *websocket.Conn) error {
	s.conn = conn
	s.instanceUid = instanceUid
	err := s.sendStatusReport()

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

// UpdateStatus applies the specified modifier function to the status report that
// will be sent next and marks the status report as pending to be sent.
func (s *Sender) UpdateStatus(modifier func(statusReport *protobufs.StatusReport)) {
	s.statusReportMutex.Lock()
	modifier(&s.statusReport)
	s.statusReportPending = true
	s.statusReportMutex.Unlock()
}

// ScheduleSend signals to the sending goroutine to send the current status report
// if it is pending. If there is no pending status report (e.g. status report was
// already sent and "pending" flag is reset) then status report will not be sent.
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
			s.sendMessages()

		case <-ctx.Done():
			break out
		}
	}

	close(s.stopped)
}

func (s *Sender) sendMessages() {
	s.sendStatusReport()
}

func (s *Sender) sendStatusReport() error {
	var statusReport *protobufs.StatusReport
	s.statusReportMutex.Lock()
	if s.statusReportPending {
		// Clone the statusReport to have a copy for sending and avoid blocking
		// future updates to statusReport field.
		statusReport = proto.Clone(&s.statusReport).(*protobufs.StatusReport)
		s.statusReportPending = false

		// Reset fields that we do not have to send unless they change before the
		// next report after this one.
		s.statusReport.RemoteConfigStatus = nil
		s.statusReport.EffectiveConfig = nil
		s.statusReport.AgentDescription = nil
	}
	s.statusReportMutex.Unlock()

	if statusReport != nil {
		msg := protobufs.AgentToServer{
			InstanceUid: s.instanceUid,
			Body: &protobufs.AgentToServer_StatusReport{
				StatusReport: statusReport,
			},
		}
		return s.sendMessage(&msg)
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
