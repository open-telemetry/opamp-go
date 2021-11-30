package client

type nopLogger struct{}

func (l *nopLogger) Debugf(format string, v ...interface{}) {}
func (l *nopLogger) Errorf(format string, v ...interface{}) {}
