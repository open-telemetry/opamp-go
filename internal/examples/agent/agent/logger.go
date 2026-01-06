package agent

import (
	"context"
	"log"
	"os"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client/types"
)

var _ types.Logger = &Logger{}

type Logger struct {
	Logger *log.Logger
}

// NewScaleLogger returns a logger that prints to stderr with passed uid as a part of the prefix.
func NewScaleLogger(uid uuid.UUID) *Logger {
	return &Logger{
		Logger: log.New(os.Stderr, "agent-"+uid.String()+": ", log.Ldate|log.Lmicroseconds|log.Lmsgprefix),
	}
}

func (l *Logger) Debugf(_ context.Context, format string, v ...interface{}) {
	l.Logger.Printf(format, v...)
}

func (l *Logger) Errorf(_ context.Context, format string, v ...interface{}) {
	l.Logger.Printf(format, v...)
}
