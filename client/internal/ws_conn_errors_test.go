//go:build !windows

package internal

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
			name: "direct ECONNRESET returns true",
			err:  syscall.Errno(syscall.ECONNRESET),
			want: true,
		},
		{
			name: "direct EPIPE returns true",
			err:  syscall.Errno(syscall.EPIPE),
			want: true,
		},
		{
			name: "direct ECONNABORTED returns true",
			err:  syscall.Errno(syscall.ECONNABORTED),
			want: true,
		},
		{
			name: "direct ENETRESET returns true",
			err:  syscall.Errno(syscall.ENETRESET),
			want: true,
		},
		{
			name: "direct ETIMEDOUT returns true",
			err:  syscall.Errno(syscall.ETIMEDOUT),
			want: true,
		},
		{
			name: "direct non-reset errno returns false",
			err:  syscall.Errno(syscall.EINVAL),
			want: false,
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
			name: "OpError wrapping EPIPE returns true",
			err: &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.EPIPE),
			},
			want: true,
		},
		{
			name: "OpError wrapping non-reset errno returns false",
			err: &net.OpError{
				Op:  "write",
				Err: syscall.Errno(syscall.EINVAL),
			},
			want: false,
		},
		{
			name: "wrapped OpError with ECONNRESET returns true",
			err:  errors.Join(errors.New("wrapper"), &net.OpError{Op: "write", Err: syscall.Errno(syscall.ECONNRESET)}),
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
