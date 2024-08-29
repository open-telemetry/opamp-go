package internal

import (
	"context"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
)

var _ types.Logger = &NopLogger{}

type NopLogger struct{}

func (l *NopLogger) Debugf(ctx context.Context, format string, v ...interface{}) {}
func (l *NopLogger) Errorf(ctx context.Context, format string, v ...interface{}) {}

type DelayLogger struct{}

func (l *DelayLogger) Debugf(ctx context.Context, format string, v ...interface{}) {
	time.Sleep(10 * time.Millisecond)
}
func (l *DelayLogger) Errorf(ctx context.Context, format string, v ...interface{}) {
	time.Sleep(10 * time.Millisecond)
}
