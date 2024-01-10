package internal

import (
	"context"

	"github.com/open-telemetry/opamp-go/client/types"
)

var _ types.Logger = &NopLogger{}

type NopLogger struct{}

func (l *NopLogger) Infof(format string, v ...interface{})                              {}
func (l *NopLogger) Debugf(format string, v ...interface{})                             {}
func (l *NopLogger) Errorf(format string, v ...interface{})                             {}
func (l *NopLogger) InfofContext(ctx context.Context, format string, v ...interface{})  {}
func (l *NopLogger) DebugfContext(ctx context.Context, format string, v ...interface{}) {}
func (l *NopLogger) ErrorfContext(ctx context.Context, format string, v ...interface{}) {}
