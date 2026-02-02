//go:build windows

package internal

import (
	"errors"
	"net"
	"syscall"
)

// isConnectionResetError returns true if the error indicates a connection was reset or broken.
func isConnectionResetError(err error) bool {
	if err == nil {
		return false
	}

	var opError *net.OpError
	if errors.As(err, &opError) {
		return isResetSyscallError(opError.Err)
	}

	// Also check if it's directly a syscall error
	return isResetSyscallError(err)
}

func isResetSyscallError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.WSAECONNRESET ||
			errno == syscall.WSAECONNABORTED
	}
	return false
}
