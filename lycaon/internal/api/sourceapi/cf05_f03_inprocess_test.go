package sourceapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

type cf05RecordingInventory struct {
	mu       sync.Mutex
	observed []sourceledger.PathRef
	passes   int
}

func (r *cf05RecordingInventory) EnsureInventory(context.Context, sourceledger.InventoryRequest) error {
	r.mu.Lock()
	r.passes++
	r.mu.Unlock()
	return nil
}

func (*cf05RecordingInventory) SuspendInventory(context.Context, string) (func(), error) {
	return func() {}, nil
}

func (*cf05RecordingInventory) InventoryState(context.Context, string, sourcebranch.ID, int) (sourceledger.InventoryState, error) {
	return sourceledger.InventoryState{Phase: sourceledger.InventoryReady, Complete: false}, nil
}

func (r *cf05RecordingInventory) ObservePaths(_ context.Context, _ string, _ []sourceledger.RootSpec, refs []sourceledger.PathRef) (int, error) {
	r.mu.Lock()
	r.observed = append(r.observed, refs...)
	r.mu.Unlock()
	return len(refs), nil
}

func (r *cf05RecordingInventory) takeObserved() []sourceledger.PathRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]sourceledger.PathRef(nil), r.observed...)
	r.observed = nil
	return out
}

func cf05F03RNG(idx int) *rand.Rand {
	sum := sha256.Sum256([]byte(fmt.Sprintf("20261127:F03:%d", idx)))
	return rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum[0:8]), binary.BigEndian.Uint64(sum[8:16])))
}

// TestCF05_F03_ObservationBeforeWalkInProcess runs 300 deterministically seeded F03 instances
// (seed 20261127) where the first catalog walk is blocked behind a barrier channel, and 1–50
// outside writes arrive during the blocked walk. Verifies observation never waits on the walk
// and every outside edit reaches history immediately.
func TestCF05_F03_ObservationBeforeWalkInProcess(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	inventory := &cf05RecordingInventory{}

	server := newSourceHandlerFixture(t, func(d *Deps) {
		d.SourceInventory = inventory
		d.WatchNeedsSeed = func(string) bool { return true }
		d.CatalogSnapshot = func(ctx context.Context, _ string, _ []sourcecatalog.Root) (sourcecatalog.Snapshot, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return sourcecatalog.Snapshot{}, ctx.Err()
			}
			return sourcecatalog.Snapshot{}, nil
		}
	})

	root := t.TempDir()
	p, err := projecttest.CreateWithRoot(t.Context(), server.Watch.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)

	walkDone := make(chan struct{})
	go func() {
		defer close(walkDone)
		server.Watch.EnsureSourceWatch(t.Context(), p.ID)
	}()

	// Wait until the first catalog walk is actively blocked inside CatalogSnapshot.
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("catalog walk barrier never entered")
	}

	const instances = 300
	for i := 0; i < instances; i++ {
		rng := cf05F03RNG(i)
		opCount := 1 + rng.IntN(50)
		if i == 0 {
			opCount = 1
		} else if i == instances-1 {
			opCount = 50
		}

		changes := make([]sourcefeed.Change, 0, opCount)
		wantPaths := make(map[string]struct{}, opCount)
		for j := 0; j < opCount; j++ {
			rel := fmt.Sprintf("during_walk/inst_%03d/file_%02d.txt", i, j)
			changes = append(changes, sourcefeed.Change{
				RootID: p.Roots[0].ID,
				Path:   rel,
			})
			wantPaths[rel] = struct{}{}
		}

		// Deliver external edits while the first catalog walk is STILL blocked!
		server.Watch.observeWorkspaceChanges(t.Context(), p, sourcefeed.ExternalBatch{Changes: changes})
		got := inventory.takeObserved()
		if len(got) != opCount {
			t.Fatalf("instance %d observed=%d want %d while first walk blocked", i, len(got), opCount)
		}
		for _, ref := range got {
			if _, ok := wantPaths[ref.Path]; !ok {
				t.Fatalf("instance %d unexpected observed path %q", i, ref.Path)
			}
		}
	}

	// Release the blocked walk and cleanly drain.
	close(release)
	<-walkDone
	server.Watch.Stop(t.Context())
	testutil.FailErr(t, "drain watch", server.Watch.Wait(t.Context()))
}
