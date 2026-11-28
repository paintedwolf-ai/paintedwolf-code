package repochange

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// TestFlushOrdersBurstEditsIntoDebouncerBeforeBarrierAcknowledges is a
// white-box ordering test (Decision 108): a burst of user-tree writes made
// immediately before w.flush(ctx) must all be present in the debouncer the
// instant the barrier acknowledges, with no sentinel path leaking through.
func TestFlushOrdersBurstEditsIntoDebouncerBeforeBarrierAcknowledges(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()

	r := newWatcherRegistry()
	t.Cleanup(r.CloseAll)

	r.Ensure(ctx, root)
	r.Seed(ctx, root, []WatchDirectory{{Path: root, EntryCount: 0}})
	testutil.WaitFor(t, "watcher coverage complete", func() bool { return r.Coverage(root).Complete })
	if !r.debounce.WaitQuiet(ctx, root, DefaultDebounce, 5*time.Second) {
		t.Fatal("initial watcher setup did not reach quiet")
	}

	key, err := filepath.Abs(root)
	testutil.FailErr(t, "abs root", err)

	r.mu.Lock()
	w := r.byDir[key]
	r.mu.Unlock()
	if w == nil {
		t.Fatalf("watcher missing for %s", key)
	}

	// Hold back debounce delivery so we can inspect debouncer.pending directly
	// the instant w.flush(ctx) acknowledges.
	r.debounce.flush(ctx)
	r.debounce.mu.Lock()
	r.debounce.delay = 5 * time.Second
	r.debounce.mu.Unlock()

	const burstCount = 20
	wantFiles := make([]string, burstCount)
	for i := range burstCount {
		name := fmt.Sprintf("burst-%02d.go", i)
		wantFiles[i] = name
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(root, name), []byte(fmt.Sprintf("package p // %d\n", i)), 0o600))
	}

	// No sleep between the burst writes and the flush barrier.
	flushCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !w.flush(flushCtx) {
		t.Fatal("w.flush returned false before barrier acknowledged")
	}

	// Immediately inspect debouncer.pending: every burst file must already be recorded.
	r.debounce.mu.Lock()
	ent := r.debounce.pending[key]
	var changes map[string]WorktreeChangeKind
	if ent != nil {
		changes = make(map[string]WorktreeChangeKind, len(ent.changes))
		for p, k := range ent.changes {
			changes[p] = k
		}
	}
	r.debounce.mu.Unlock()

	if len(changes) == 0 {
		t.Fatalf("debouncer pending[%s] is empty immediately after w.flush acknowledged", key)
	}
	for p := range changes {
		if strings.Contains(p, "pw-fsevents-barrier-") || strings.Contains(p, "pw-inotify-barrier-") || strings.HasPrefix(filepath.Base(p), "s-") {
			t.Fatalf("sentinel path leaked into debouncer changes: %q (all=%v)", p, changes)
		}
	}
	if changes["."] == WorktreeChangeResync {
		return
	}
	var missing []string
	for _, name := range wantFiles {
		if _, ok := changes[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("missing %d/%d burst edits in debouncer when barrier acknowledged: missing=%v got=%v",
			len(missing), burstCount, missing, changes)
	}
}
