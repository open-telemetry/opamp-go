package scale

import (
	"context"
	"log"
	"os"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client/types"
)

var _ types.Logger = &Logger{}

type Logger struct {
	logger *log.Logger
}

func NewLogger(uid uuid.UUID) *Logger {
	return &Logger{
		logger: log.New(os.Stderr, "agent-"+uid.String()+": ", log.Ldate|log.Lmicroseconds|log.Lmsgprefix),
	}
}

func (l *Logger) Debugf(ctx context.Context, format string, v ...interface{}) {
	l.logger.Printf(format, v...)
}

func (l *Logger) Errorf(ctx context.Context, format string, v ...interface{}) {
	l.logger.Printf(format, v...)
}
