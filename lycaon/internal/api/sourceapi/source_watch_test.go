package sourceapi

import (
	"context"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

// countingInventory records the full passes the handler schedules.
type countingInventory struct {
	mu     sync.Mutex
	passes int
}

func (f *countingInventory) EnsureInventory(context.Context, sourceledger.InventoryRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passes++
	return nil
}

func (*countingInventory) SuspendInventory(context.Context, string) (func(), error) {
	return func() {}, nil
}

func (*countingInventory) InventoryState(context.Context, string, sourcebranch.ID, int) (sourceledger.InventoryState, error) {
	return sourceledger.InventoryState{Phase: sourceledger.InventoryReady, Complete: true}, nil
}

func (*countingInventory) ObservePaths(context.Context, string, []sourceledger.RootSpec, []sourceledger.PathRef) (int, error) {
	return 0, nil
}

func (f *countingInventory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.passes
}

// A batch that names its paths moves history for those paths and leaves the
// full pass to windows the watcher could not name.
func TestObserveWorkspaceChangesRoutesNamedBatchesToPaths(t *testing.T) {
	inventory := &countingInventory{}
	server := newSourceHandlerFixture(t, func(d *Deps) { d.SourceInventory = inventory })
	p, err := project.CreateWithRoot(t.Context(), server.Workspace.ProjectRegistry, t.TempDir())
	testutil.FailErr(t, "create project", err)

	server.Watch.observeWorkspaceChanges(t.Context(), p, sourcefeed.ExternalBatch{
		Changes: []sourcefeed.Change{{RootID: p.Roots[0].ID, Path: "a.txt"}},
	})
	server.Views.background.Wait(context.Background())
	if got := inventory.count(); got != 0 {
		t.Fatalf("a named batch scheduled %d full passes", got)
	}

	server.Watch.observeWorkspaceChanges(t.Context(), p, sourcefeed.ExternalBatch{Resync: true})
	server.Views.background.Wait(context.Background())
	if got := inventory.count(); got != 1 {
		t.Fatalf("a resync scheduled %d full passes, want 1", got)
	}
}
