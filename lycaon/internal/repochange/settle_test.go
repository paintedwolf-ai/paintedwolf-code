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
	if repochange.SettleRootStatus(ctx, root, 50*time.Millisecond, 2*time.Second) != repochange.SettleQuiet {
		t.Fatal("SettleRootStatus did not report quiet on a quiet tree")
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
	if repochange.SettleRootStatus(ctx, root, 50*time.Millisecond, 200*time.Millisecond) == repochange.SettleQuiet {
		t.Fatal("SettleRootStatus reported quiet while debounce was held back past maxWait")
	}

	restore()
	if repochange.SettleRootStatus(ctx, root, 50*time.Millisecond, 2*time.Second) != repochange.SettleQuiet {
		t.Fatal("SettleRootStatus did not report quiet after debounce was released")
	}
}
