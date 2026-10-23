package db

import (
	"errors"
	"syscall"
	"testing"
)

func TestRebuildFailurePreservesOperationalCause(t *testing.T) {
	for _, cause := range []error{syscall.ENOSPC, syscall.EIO, syscall.EACCES} {
		err := classifyUpgradeFailure(cause, 4)
		if !errors.Is(err, cause) || errors.Is(err, ErrStoreIncompatible) {
			t.Fatalf("operational error was replaced by schema/corruption claim: %v", err)
		}
	}
}
