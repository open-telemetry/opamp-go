package internal

type NopLogger struct{}

func (l *NopLogger) Debugf(format string, v ...interface{}) {}
func (l *NopLogger) Errorf(format string, v ...interface{}) {}
