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
		return errno == syscall.WSAECONNRESET || // connection reset by peer (windows specific)
			errno == syscall.WSAECONNABORTED || // local software caused connection abort (windows specific)
			errno == syscall.ENETRESET || // connection reset on network level
			errno == syscall.ETIMEDOUT // connection timed out
	}
	return false
}
