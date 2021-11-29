package types

type Logger interface {
	Debugf(format string, v ...interface{})
	Errorf(format string, v ...interface{})
}
