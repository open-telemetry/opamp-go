package types

import "context"

// Logger is the logging interface used by the OpAMP Client.
type Logger interface {
	Infof(ctx context.Context, format string, v ...interface{})
	Debugf(ctx context.Context, format string, v ...interface{})
	Errorf(ctx context.Context, format string, v ...interface{})
}
