//go:build !windows

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

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() { // connection timed out
			return true
		}
		return isResetSyscallError(netErr)
	}

	// Also check if it's directly a syscall error
	return isResetSyscallError(err)
}

func isResetSyscallError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ECONNRESET || // connection reset by peer
			errno == syscall.EPIPE || // broken pipe
			errno == syscall.ECONNABORTED || // local software caused connection abort
			errno == syscall.ENETRESET // connection reset on network level
	}
	return false
}
