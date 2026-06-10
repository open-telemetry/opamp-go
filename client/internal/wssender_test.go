package internal

import (
	"net"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	sharedinternal "github.com/open-telemetry/opamp-go/internal"
)

const (
	senderStopTimeout = 3 * time.Second
	largeMessageSize  = 1024 * 1024
)

// connErr wraps a net.Conn to inject a configurable error on Write.
// Used by both gorilla and coder sender tests to simulate connection failures.
type connErr struct {
	net.Conn
	failWrite atomic.Bool
	writeErr  error
}

func (c *connErr) Write(b []byte) (n int, err error) {
	if c.failWrite.Load() {
		return 0, c.writeErr
	}
	return c.Conn.Write(b)
}

func newConnAbortedErrConn(c net.Conn) *connErr {
	var errno syscall.Errno
	if runtime.GOOS == "windows" {
		errno = syscall.Errno(wsaECONNABORTED)
	} else {
		errno = syscall.ECONNABORTED
	}
	return &connErr{Conn: c, writeErr: &net.OpError{Op: "write", Err: errno}}
}

func TestWSSenderSetHeartbeatInterval(t *testing.T) {
	sender := NewSender(&sharedinternal.NopLogger{})

	// Default interval should be 30s as per OpAMP Specification
	assert.Equal(t, int64((30 * time.Second).Milliseconds()), sender.heartbeatIntervalMs.Load())

	// negative interval is invalid for http sender
	assert.Error(t, sender.SetHeartbeatInterval(-1))
	assert.Equal(t, int64((30 * time.Second).Milliseconds()), sender.heartbeatIntervalMs.Load())

	// zero is valid for ws sender
	assert.NoError(t, sender.SetHeartbeatInterval(0))
	assert.Equal(t, int64(0), sender.heartbeatIntervalMs.Load())

	var expected int64 = 10000
	assert.NoError(t, sender.SetHeartbeatInterval(time.Duration(expected)*time.Millisecond))
	assert.Equal(t, expected, sender.heartbeatIntervalMs.Load())
}
