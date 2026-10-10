package api

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type workspaceInventoryService struct {
	recordingInventoryService
	state sourceledger.InventoryState
}

func (f *workspaceInventoryService) InventoryState(context.Context, string, sourcebranch.ID, int) (sourceledger.InventoryState, error) {
	return f.state, nil
}

// A parked project's watch rebinds on activation, and the rebinding
// reconciles the inventory it may have missed while unwatched.
func TestSourceWatchActivationReconcilesInventory(t *testing.T) {
	inventory := &workspaceInventoryService{state: sourceledger.InventoryState{Phase: sourceledger.InventoryUninitialized}}
	srv := newTestServer(t, func(d *Dependencies) {
		d.Source.WatchNeedsSeed = func(string) bool { return false }
		d.Source.SourceInventory = inventory
	})
	p := createProjectForTest(t, srv, t.TempDir())
	srv.background.Wait(context.Background())
	t.Cleanup(func() { sourcefeed.StopProjectWatch(context.Background(), p.ID) })
	setupStarts := len(inventory.snapshot())
	inventory.state = sourceledger.InventoryState{
		Phase: sourceledger.InventoryReady, RequestedGeneration: p.RootsGeneration,
		CompletedGeneration: p.RootsGeneration, Complete: true,
	}
	for attempt := range 2 {
		sourcefeed.StopProjectWatch(context.Background(), p.ID)
		srv.Sources.Watch.ScheduleSourceWatch(t.Context(), p.ID)
		srv.background.Wait(context.Background())
		if got := len(inventory.snapshot()) - setupStarts; got != attempt+1 {
			t.Fatalf("inventory starts after watch binding = %d, want %d", got, attempt+1)
		}
	}
}
