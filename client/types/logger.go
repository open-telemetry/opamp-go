package types

// Logger is the logging interface used by the OpAMP Client.
type Logger interface {
	Debugf(format string, v ...interface{})
	Errorf(format string, v ...interface{})
	Fatalf(format string, v ...interface{})
}
