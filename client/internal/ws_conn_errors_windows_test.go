//go:build windows

package internal

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsConnectionResetErrorWindows(t *testing.T) {
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
			name: "direct connection reset by peer",
			err:  syscall.Errno(syscall.WSAECONNRESET),
			want: true,
		},
		{
			name: "direct software caused connection abort",
			err:  syscall.Errno(syscall.WSAECONNABORTED),
			want: true,
		},
		{
			name: "direct connection reset on network level",
			err:  syscall.Errno(syscall.ENETRESET),
			want: true,
		},
		{
			name: "direct connection timed out",
			err:  syscall.Errno(syscall.ETIMEDOUT),
			want: true,
		},
		{
			name: "OpError connection reset by peer",
			err: &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.WSAECONNRESET),
			},
			want: true,
		},
		{
			name: "OpError software caused connection abort",
			err: &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.WSAECONNABORTED),
			},
			want: true,
		},
		{
			name: "OpError connection reset on network level",
			err: &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.ENETRESET),
			},
			want: true,
		},
		{
			name: "OpError connection timed out",
			err: &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.ETIMEDOUT),
			},
			want: true,
		},
		{
			name: "OpError non-reset error",
			err: &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.WSAEACCES),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isConnectionResetError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}
