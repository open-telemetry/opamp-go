package types

import "context"

// Logger is the logging interface used by the OpAMP Client.
type Logger interface {
	Infof(format string, v ...interface{})
	Debugf(format string, v ...interface{})
	Errorf(format string, v ...interface{})
	InfofContext(ctx context.Context, format string, v ...interface{})
	DebugfContext(ctx context.Context, format string, v ...interface{})
	ErrorfContext(ctx context.Context, format string, v ...interface{})
}
