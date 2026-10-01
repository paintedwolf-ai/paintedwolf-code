package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPromoteFileLockSerializesWritersAcrossServiceInstances(t *testing.T) {
	hostData := t.TempDir()
	first, err := acquirePromoteFileLock(t.Context(), hostData)
	testutil.FailErr(t, "acquire first promote lock", err)

	blockedCtx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if _, err := acquirePromoteFileLock(blockedCtx, hostData); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("competing promote lock = %v", err)
	}
	first.release()
	second, err := acquirePromoteFileLock(t.Context(), hostData)
	testutil.FailErr(t, "acquire promote lock after release", err)
	second.release()
}
