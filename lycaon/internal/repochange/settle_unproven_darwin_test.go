//go:build darwin && cgo

package repochange

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFSEventsNoSentinelAndMultiPumpReportSettleUnproven(t *testing.T) {
	ctx := t.Context()

	t.Run("cross-volume no sentinel", func(t *testing.T) {
		deny := func(string, string) bool { return false }
		previous := SentinelDeviceCheckForTest.Swap(&deny)
		t.Cleanup(func() { SentinelDeviceCheckForTest.Store(previous) })

		root := t.TempDir()
		r := newWatcherRegistry()
		t.Cleanup(r.CloseAll)

		r.Ensure(ctx, root)
		r.Seed(ctx, root, []WatchDirectory{{Path: root, EntryCount: 0}})
		testutil.WaitFor(t, "watcher coverage complete", func() bool { return r.Coverage(root).Complete })

		if got := r.SettleStatus(ctx, root, 50*time.Millisecond, time.Second); got != SettleUnproven {
			t.Fatalf("SettleStatus without sentinel = %v, want SettleUnproven", got)
		}
	})

	t.Run("secondary stream pump", func(t *testing.T) {
		root := t.TempDir()
		secondary := t.TempDir()

		r := newWatcherRegistry()
		t.Cleanup(r.CloseAll)

		r.Ensure(ctx, root)
		r.Seed(ctx, root, []WatchDirectory{{Path: root, EntryCount: 0}})
		testutil.WaitFor(t, "watcher coverage complete", func() bool { return r.Coverage(root).Complete })

		key, err := filepath.Abs(root)
		testutil.FailErr(t, "abs root", err)
		r.mu.Lock()
		w := r.byDir[key]
		r.mu.Unlock()
		if w == nil {
			t.Fatalf("watcher missing for %s", key)
		}
		testutil.FailErr(t, "add secondary stream root", w.watcher.Add(secondary))

		if got := r.SettleStatus(ctx, root, 50*time.Millisecond, time.Second); got != SettleUnproven {
			t.Fatalf("SettleStatus with secondary pump = %v, want SettleUnproven", got)
		}
	})
}
