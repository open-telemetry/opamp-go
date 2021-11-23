package types

type Logger interface {
	Infof(format string, v ...interface{})
	Errorf(format string, v ...interface{})
}
