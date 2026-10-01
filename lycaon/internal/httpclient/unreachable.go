package httpclient

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
)

// Unreachable reports structural transport failures, not HTTP error responses.
func Unreachable(err error) bool {
	if err == nil {
		return false
	}

	// Detect client, context, and socket deadlines.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}

	// Detect wrapped dial, TLS, DNS, and read failures.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	// A bare net.Error match would misclassify non-timeout syscall errors.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// Detect refused, unroutable, reset, and broken connections.
	for _, errno := range []syscall.Errno{
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.ECONNABORTED,
		syscall.EHOSTUNREACH,
		syscall.EHOSTDOWN,
		syscall.ENETUNREACH,
		syscall.ENETDOWN,
		syscall.ENETRESET,
		syscall.EPIPE,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}

	return false
}
