package sourcefeed

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectStopPreservesReboundRootWhileOldCallbackDrains(t *testing.T) {
	root, projectID := t.TempDir(), t.Name()
	roots := []RootSpec{{ID: "root", WorkspaceID: "workspace", Path: root}}
	t.Cleanup(func() { testutil.FailErr(t, "stop rebound project", StopProjectWatch(context.Background(), projectID)) })
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	block := sync.OnceFunc(func() { close(entered); <-release })
	EnsureProjectWatch(t.Context(), nil, projectID, "", roots, func(context.Context, string, ExternalBatch) { block() })
	watchRegMu.Lock()
	old := watchers[watchKey{projectID, ""}]
	watchRegMu.Unlock()
	go func() {
		defer close(completed)
		old.flushExternalChanges(t.Context(), pendingExternalBatch{resync: true})
	}()
	testutil.AwaitClosed(t, "old callback starts", entered)
	stopped := make(chan error, 1)
	go func() { stopped <- StopProjectWatch(t.Context(), projectID) }()
	testutil.WaitFor(t, "old routing is removed", func() bool {
		watchRegMu.Lock()
		defer watchRegMu.Unlock()
		return watchers[watchKey{projectID, ""}] == nil
	})
	changed, initial := make(chan struct{}, 1), make(chan struct{}, 1)
	EnsureProjectWatch(t.Context(), nil, projectID, "", roots, func(_ context.Context, _ string, batch ExternalBatch) {
		if batch.Resync {
			select {
			case initial <- struct{}{}:
			default:
			}
		}
		for _, change := range batch.Changes {
			if change.Path == "changed.txt" {
				select {
				case changed <- struct{}{}:
				default:
				}
			}
		}
	})
	testutil.WaitFor(t, "rebound root is covered", func() bool { return repochange.Coverage(root).Complete })
	testutil.Receive(t, "rebound startup resync is delivered", initial)
	unblock()
	testutil.AwaitClosed(t, "old callback completes", completed)
	testutil.FailErr(t, "old project stop completes", testutil.Receive(t, "old project stop", stopped))
	if !repochange.Coverage(root).Watching {
		t.Fatal("old project stop closed the rebound root stream")
	}
	testutil.FailErr(t, "write after old stop", os.WriteFile(filepath.Join(root, "changed.txt"), []byte("new binding\n"), 0o600))
	testutil.Receive(t, "rebound stream observes the write", changed)
}
