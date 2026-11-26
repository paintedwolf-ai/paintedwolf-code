//go:build linux

package lineage

import (
	"errors"
	"syscall"
	"testing"
)

func TestInterruptedReceivesAreRepeatedAndOtherErrorsReturned(t *testing.T) {
	results := []error{syscall.EINTR, syscall.EINTR, nil}
	calls := 0
	n, err := retryInterrupted(func() (int, error) {
		err := results[calls]
		calls++
		if err != nil {
			return -1, err
		}
		return 7, nil
	})
	if n != 7 || err != nil || calls != 3 {
		t.Fatalf("receive = %d, %v after %d calls, want 7 after 3", n, err, calls)
	}
	if _, err := retryInterrupted(func() (int, error) { return -1, syscall.EAGAIN }); !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("timeout = %v, want EAGAIN returned", err)
	}
}
