package sourceledger

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSuspendInventoryCancelsAllBranchesAndBlocksAdmission(t *testing.T) {
	st, ctx := openLedger(t)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	testdbseed.InsertProject(t, st.sqlDB, "p2")
	started := make(chan string, 3)
	st.Inventory.inventoryReconcile = func(ctx context.Context, projectID string, _ []RootSpec) (int, error) {
		started <- projectID
		<-ctx.Done()
		return 0, ctx.Err()
	}
	otherCtx, cancelOther := context.WithCancel(ctx)
	defer cancelOther()
	done := make(chan error, 2)
	for _, branch := range []string{"", "worker"} {
		go func() {
			done <- st.Inventory.EnsureInventory(ctx, InventoryRequest{ProjectID: "p1", RootsGeneration: 1,
				Roots: []RootSpec{{ID: "r1", BranchID: sourcebranch.ID(branch), Path: "/first"}}})
		}()
	}
	otherDone := make(chan error, 1)
	go func() {
		otherDone <- st.Inventory.EnsureInventory(otherCtx, InventoryRequest{ProjectID: "p2", RootsGeneration: 1,
			Roots: []RootSpec{{ID: "r2", Path: "/second"}}})
	}()
	for range 3 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("inventory did not start")
		}
	}
	drainCtx, cancelDrain := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDrain()
	resume, err := st.Inventory.SuspendInventory(drainCtx, "p1")
	defer resume()
	testutil.FailErr(t, "suspend inventory", err)
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("retired inventory = %v, want cancellation", err)
		}
	}
	select {
	case err := <-otherDone:
		t.Fatalf("unrelated inventory stopped: %v", err)
	default:
	}
	if err := st.Inventory.EnsureInventory(ctx, InventoryRequest{ProjectID: "p1", RootsGeneration: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("suspended admission = %v, want cancellation", err)
	}
	cancelOther()
	<-otherDone
	resume()
	resume()
	st.Inventory.inventoryReconcile = func(context.Context, string, []RootSpec) (int, error) { return 0, nil }
	testutil.FailErr(t, "resume new generation", st.Inventory.EnsureInventory(ctx, InventoryRequest{ProjectID: "p1", RootsGeneration: 2}))
}

func TestEnsureInventoryCoalescesAndRunsNewestRootsGeneration(t *testing.T) {
	st, ctx := openLedger(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	st.Inventory.inventoryReconcile = func(_ context.Context, _ string, roots []RootSpec) (int, error) {
		call := calls.Add(1)
		if call == 1 {
			close(started)
			<-release
		}
		return len(roots), nil
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- st.Inventory.EnsureInventory(ctx, InventoryRequest{
			ProjectID: "p1", RootsGeneration: 1,
			Roots: []RootSpec{{ID: "r1", Path: "/first"}},
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first inventory generation did not start")
	}

	testutil.FailErr(t, "join same generation", st.Inventory.EnsureInventory(ctx, InventoryRequest{
		ProjectID: "p1", RootsGeneration: 1,
		Roots: []RootSpec{{ID: "r1", Path: "/duplicate"}},
	}))
	testutil.FailErr(t, "queue next generation", st.Inventory.EnsureInventory(ctx, InventoryRequest{
		ProjectID: "p1", RootsGeneration: 2,
		Roots: []RootSpec{{ID: "r1", Path: "/next"}, {ID: "r2", Path: "/added"}},
	}))
	close(release)
	select {
	case err := <-firstDone:
		testutil.FailErr(t, "finish inventory", err)
	case <-time.After(time.Second):
		t.Fatal("inventory generations did not finish")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("inventory calls = %d, want one active plus one newer generation", got)
	}
	state, err := st.Inventory.InventoryState(ctx, "p1", "", 2)
	testutil.FailErr(t, "inventory state", err)
	if !state.Complete || state.Phase != InventoryReady || state.CompletedGeneration != 2 || state.FileCount != 2 {
		t.Fatalf("inventory state = %+v", state)
	}
}

func TestInventoryStateIsUninitializedWithoutDurableRow(t *testing.T) {
	st, ctx := openLedger(t)
	state, err := st.Inventory.InventoryState(ctx, "p1", "", 3)
	testutil.FailErr(t, "inventory state", err)
	if state.Complete || state.Phase != InventoryUninitialized || state.CompletedGeneration != -1 {
		t.Fatalf("inventory state = %+v", state)
	}
}

func TestInventoryJobRetirementCannotDeleteNewerJob(t *testing.T) {
	st, _ := openLedger(t)
	completed := InventoryRequest{ProjectID: "p1", RootsGeneration: 1, requestedEpoch: "epoch-1", serial: 1}
	retiring := &inventoryJob{request: completed, done: make(chan struct{})}
	st.Inventory.inventoryJobs["p1"] = retiring

	if !st.Inventory.settleInventoryJob("p1", retiring, completed, nil) {
		t.Fatal("completed inventory job did not settle")
	}
	select {
	case <-retiring.done:
	default:
		t.Fatal("settled job did not release its waiters")
	}
	newer := &inventoryJob{request: InventoryRequest{
		ProjectID: "p1", RootsGeneration: 2, requestedEpoch: "epoch-2", serial: 2,
	}, done: make(chan struct{})}
	st.Inventory.inventoryJobs["p1"] = newer
	st.Inventory.releaseInventoryJob("p1", retiring)
	if st.Inventory.inventoryJobs["p1"] != newer {
		t.Fatal("retiring job deleted the newer inventory record")
	}
}

func TestEnsureInventoryCompletesForEmptyFolder(t *testing.T) {
	st, ctx := openLedger(t)
	empty := t.TempDir()
	req := InventoryRequest{
		ProjectID: "p1", RootsGeneration: 1,
		Roots: []RootSpec{{ID: "r1", Path: empty}},
	}
	done := make(chan error, 1)
	go func() { done <- st.Inventory.EnsureInventory(ctx, req) }()
	select {
	case err := <-done:
		testutil.FailErr(t, "ensure inventory", err)
	case <-time.After(5 * time.Second):
		t.Fatal("empty-folder inventory did not finish within 5s")
	}
	state, err := st.Inventory.InventoryState(ctx, "p1", "", 1)
	testutil.FailErr(t, "inventory state", err)
	if !state.Complete || state.Phase != InventoryReady {
		t.Fatalf("inventory state = %+v", state)
	}
	if state.FileCount != 0 {
		t.Fatalf("file count = %d, want 0", state.FileCount)
	}
}

func TestEnsureInventoryStaysCompleteWhenRescheduledForEmptyFolder(t *testing.T) {
	st, ctx := openLedger(t)
	var calls atomic.Int32
	st.Inventory.inventoryReconcile = func(context.Context, string, []RootSpec) (int, error) {
		calls.Add(1)
		return 0, nil
	}
	empty := t.TempDir()
	req := InventoryRequest{
		ProjectID: "p1", RootsGeneration: 1,
		Roots: []RootSpec{{ID: "r1", Path: empty}},
	}
	testutil.FailErr(t, "first ensure", st.Inventory.EnsureInventory(ctx, req))
	state, err := st.Inventory.InventoryState(ctx, "p1", "", 1)
	testutil.FailErr(t, "state after first", err)
	if !state.Complete {
		t.Fatalf("first inventory incomplete: %+v", state)
	}
	for i := 0; i < 5; i++ {
		testutil.FailErr(t, "reschedule", st.Inventory.EnsureInventory(ctx, req))
		state, err = st.Inventory.InventoryState(ctx, "p1", "", 1)
		testutil.FailErr(t, "state", err)
		if !state.Complete || state.Phase != InventoryReady {
			t.Fatalf("reschedule %d left inventory incomplete: %+v", i, state)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("inventory reconciliations = %d, want 1 within the revalidation interval", got)
	}
}

func TestEnsureInventoryRevalidatesIncompleteWatcherCoverageAfterBound(t *testing.T) {
	st, ctx := openLedger(t)
	var calls atomic.Int32
	now := time.Now().UTC()
	st.Inventory.inventoryNow = func() time.Time { return now }
	st.Inventory.inventoryReconcile = func(context.Context, string, []RootSpec) (int, error) {
		calls.Add(1)
		return 0, nil
	}
	empty := t.TempDir()
	req := InventoryRequest{
		ProjectID: "p1", RootsGeneration: 1,
		Roots: []RootSpec{{ID: "r1", Path: empty}},
	}
	testutil.FailErr(t, "first ensure", st.Inventory.EnsureInventory(ctx, req))
	state, err := st.Inventory.InventoryState(ctx, "p1", "", 1)
	testutil.FailErr(t, "state after first ensure", err)
	now = state.CompletedAt
	now = now.Add(repochange.CoverageRevalidationInterval - time.Second)
	testutil.FailErr(t, "fresh ensure", st.Inventory.EnsureInventory(ctx, req))
	if got := calls.Load(); got != 1 {
		t.Fatalf("fresh inventory reconciliations = %d, want 1", got)
	}
	now = now.Add(2 * time.Second)
	testutil.FailErr(t, "stale ensure", st.Inventory.EnsureInventory(ctx, req))
	if got := calls.Load(); got != 2 {
		t.Fatalf("stale inventory reconciliations = %d, want 2", got)
	}
}

func TestEnsureInventoryReopensAfterEpochAdvance(t *testing.T) {
	st, ctx := openLedger(t)
	empty := t.TempDir()
	req := InventoryRequest{
		ProjectID: "p1", RootsGeneration: 1,
		Roots: []RootSpec{{ID: "r1", Path: empty}},
	}
	testutil.FailErr(t, "first ensure", st.Inventory.EnsureInventory(ctx, req))
	state, err := st.Inventory.InventoryState(ctx, "p1", "", 1)
	testutil.FailErr(t, "state after first", err)
	if !state.Complete {
		t.Fatalf("first inventory incomplete: %+v", state)
	}
	repochange.Advance(empty)
	testutil.FailErr(t, "re-ensure", st.Inventory.EnsureInventory(ctx, req))
	state, err = st.Inventory.InventoryState(ctx, "p1", "", 1)
	testutil.FailErr(t, "state after re-ensure", err)
	if !state.Complete || state.Phase != InventoryReady {
		t.Fatalf("want complete after re-ensure, got %+v", state)
	}
}
