package main

import (
	"context"

	"github.com/open-telemetry/opamp-go/client/types"
)

var _ types.Logger = &NOPLogger{}

type NOPLogger struct{}

func (*NOPLogger) Debugf(_ context.Context, format string, v ...interface{}) {}
func (*NOPLogger) Errorf(_ context.Context, format string, v ...interface{}) {}
