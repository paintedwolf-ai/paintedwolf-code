package worker

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNotifyMergeWorktreeChanged(t *testing.T) {
	dir := t.TempDir()
	abs, err := filepath.Abs(dir)
	testutil.FailErr(t, "abs", err)

	var mu sync.Mutex
	var kinds []repochange.Kind
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != abs {
			return
		}
		mu.Lock()
		kinds = append(kinds, ev.Kind)
		mu.Unlock()
	})

	notifyMergeWorktreeChanged(t.Context(), &api.WorkerTask{WorkspacePath: dir}, []projectroot.RootRef{{Path: dir}})
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) != 1 || kinds[0] != repochange.WorktreeChanged {
		t.Fatalf("kinds = %v want [WorktreeChanged]", kinds)
	}
}
