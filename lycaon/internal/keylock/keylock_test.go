package keylock

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCanceledWaiterDoesNotReleaseOwnersLock(t *testing.T) {
	var group Group
	release, err := group.Acquire(t.Context(), "root")
	testutil.FailErr(t, "acquire owner", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := group.Acquire(ctx, "root"); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error=%v", err)
	}
	other, err := group.Acquire(t.Context(), "other")
	testutil.FailErr(t, "independent key", err)
	other()
	release()
	if len(group.entries) != 0 {
		t.Fatalf("idle keys retained: %d", len(group.entries))
	}
}
