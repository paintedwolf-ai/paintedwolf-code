package repochange_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSettleRootFlushesAndFailsClosedWhenUnsettled(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	repochangetest.Watch(t, root)
	repochangetest.Acquire(t)

	target := filepath.Join(root, "file.go")
	testutil.FailErr(t, "write initial", os.WriteFile(target, []byte("package p\n"), 0o600))
	if !repochange.SettleRoot(ctx, root, 50*time.Millisecond, 2*time.Second) {
		t.Fatal("SettleRoot returned false on a quiet tree")
	}

	restoreDebounce := repochange.UseDebounce(ctx, 5*time.Second)
	restored := false
	restore := func() {
		if !restored {
			restored = true
			restoreDebounce()
		}
	}
	t.Cleanup(restore)

	testutil.FailErr(t, "write under held debounce", os.WriteFile(target, []byte("package p // edited\n"), 0o600))
	if repochange.SettleRoot(ctx, root, 250*time.Millisecond, 500*time.Millisecond) {
		t.Fatal("SettleRoot reported settled while debounce was held back past maxWait")
	}

	restore()
	if !repochange.SettleRoot(ctx, root, 50*time.Millisecond, 2*time.Second) {
		t.Fatal("SettleRoot returned false after debounce was released")
	}
}
