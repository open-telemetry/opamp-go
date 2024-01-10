package supervisor

import "log"

type Logger struct {
	Logger *log.Logger
}

func (l *Logger) Debugf(format string, v ...interface{}) {
	l.Logger.Printf(format, v...)
}

func (l *Logger) Errorf(format string, v ...interface{}) {
	l.Logger.Printf(format, v...)
}

func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.Logger.Fatalf(format, v...)
}
