package sourceapi

import (
	"errors"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcetree"
)

func TestSourcePreparationFailureSeparatesStorageFromViewCapacity(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           error
		code, message string
	}{
		{"disk", syscall.ENOSPC, "resource_limit", sourceStorageFullMessage},
		{"wrapped preparation", &sourcetree.PreparationError{Err: syscall.EDQUOT}, "resource_limit", sourceStorageFullMessage},
		{"views", pagedview.ErrBudget, "resource_limit", sourceViewCapacityMessage},
		{"expired", pagedview.ErrExpired, "expired", "The retained source presentation expired. Reopen it to continue."},
		{"other", errors.New("internal cause"), "preparation_failed", "Tree unavailable."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := sourcePreparationFailure(tc.err, "Tree unavailable.")
			if result.Code != tc.code || result.Message != tc.message {
				t.Fatalf("failure = %+v", result)
			}
		})
	}
}
