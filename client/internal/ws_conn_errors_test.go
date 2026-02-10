//go:build !windows

package internal

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

const wsaECONNABORTED = 10053

func TestIsConnectionResetError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil returns false",
			err:  nil,
			want: false,
		},
		{
			name: "generic error returns false",
			err:  errors.New("some error"),
			want: false,
		},
		{
			name: "direct EPIPE returns true",
			err:  syscall.Errno(syscall.EPIPE),
			want: true,
		},
		{
			name: "OpError wrapping ECONNRESET returns true",
			err: &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.ECONNRESET),
			},
			want: true,
		},
		{
			name: "test timeout error",
			err: &net.DNSError{
				IsTimeout: true,
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isConnectionResetError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}
