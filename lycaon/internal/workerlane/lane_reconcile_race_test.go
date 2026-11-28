package workerlane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
)

type barrierWorkspaces struct {
	inner     *workspace.Manager
	onCreated func(viewID string, binding *workspace.Binding) error
}

func (w *barrierWorkspaces) CreateWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, viewID string) (*workspace.Binding, workspace.SandboxLayout, error) {
	binding, layout, err := w.inner.CreateWorkerBranch(ctx, roots, activeRootID, viewID)
	if err != nil {
		return nil, workspace.SandboxLayout{}, err
	}
	if w.onCreated != nil {
		if hookErr := w.onCreated(viewID, binding); hookErr != nil {
			return nil, workspace.SandboxLayout{}, hookErr
		}
	}
	return binding, layout, nil
}

func (w *barrierWorkspaces) CreateStackedWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, viewID, parentCheckpointID string) (*workspace.Binding, workspace.SandboxLayout, error) {
	return w.inner.CreateStackedWorkerBranch(ctx, roots, activeRootID, viewID, parentCheckpointID)
}

func (w *barrierWorkspaces) EvictWorkerBranch(ctx context.Context, branchRoot string) error {
	return w.inner.EvictWorkerBranch(ctx, branchRoot)
}

type realReconcileFixture struct {
	sandboxRoot string
	primaryDir  string
	roots       []projectroot.RootRef
	records     *workerrecords.Store
	mgr         *workspace.Manager
	ws          *barrierWorkspaces
	lanes       *Service
	actor       workerrecords.Actor
	workerID    string
	siblingID   string
	siblingRoot string
	staleRoot   string
	staleMeta   string
}

func newRealReconcileFixture(t *testing.T) *realReconcileFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "lane-reconcile.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	records := workerrecords.New(sqlDB)
	actor := workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}

	sandboxRoot := t.TempDir()
	primaryDir := t.TempDir()
	testutil.FailErr(t, "seed primary file", os.WriteFile(filepath.Join(primaryDir, "hello.txt"), []byte("hello\n"), 0o644))
	roots := []projectroot.RootRef{{ID: projectroot.SingleRootID, Label: "primary", Path: primaryDir, IsPrimary: true}}

	mgr := workspace.NewManager(sandboxRoot, platform.NewSelector(platform.Config{}), workspace.Filesystem{})
	ws := &barrierWorkspaces{inner: mgr}
	lanes := New(ws, records)

	// Create a live sibling worker with an already-prepared lane.
	sibling, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "create-sibling"}, workerrecords.WorkerSpec{
		AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite,
	})
	testutil.FailErr(t, "create sibling worker", err)
	siblingBinding, err := lanes.Admit(t.Context(), Request{
		Actor: actor, Op: workerrecords.Op{ID: "admit-sibling"}, WorkerID: sibling.WorkerID,
		Facts: Facts{Write: true}, Roots: roots, ActiveRootID: projectroot.SingleRootID,
	})
	testutil.FailErr(t, "admit sibling", err)
	t.Cleanup(func() {
		_ = mgr.DestroyWorkerWorkspace(context.Background(), &workspace.Binding{ID: laneViewID(sibling.WorkerID, siblingBinding.Generation), Root: siblingBinding.Root})
	})

	// Create the target worker whose preparation will race with reconcile.
	target, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "create-target"}, workerrecords.WorkerSpec{
		AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite,
	})
	testutil.FailErr(t, "create target worker", err)

	// Seed one genuinely stale sandbox (both tree and metadata).
	staleRoot := enginepaths.JobBranchDir(sandboxRoot, primaryDir, "stale-worker.1")
	staleMeta := enginepaths.MetaDirForBranchRoot(staleRoot)
	testutil.FailErr(t, "mkdir stale tree", os.MkdirAll(staleRoot, 0o755))
	testutil.FailErr(t, "write stale file", os.WriteFile(filepath.Join(staleRoot, "old.txt"), []byte("stale"), 0o644))
	testutil.FailErr(t, "mkdir stale meta", os.MkdirAll(staleMeta, 0o700))
	testutil.FailErr(t, "write stale meta", os.WriteFile(filepath.Join(staleMeta, "state.json"), []byte(`{"strategy":"direct_copy"}`), 0o600))

	return &realReconcileFixture{
		sandboxRoot: sandboxRoot,
		primaryDir:  primaryDir,
		roots:       roots,
		records:     records,
		mgr:         mgr,
		ws:          ws,
		lanes:       lanes,
		actor:       actor,
		workerID:    target.WorkerID,
		siblingID:   sibling.WorkerID,
		siblingRoot: siblingBinding.Root,
		staleRoot:   staleRoot,
		staleMeta:   staleMeta,
	}
}

func (f *realReconcileFixture) loadRetainedBranches(ctx context.Context) (map[string]struct{}, error) {
	roots, err := f.records.OpenLaneRoots(ctx)
	if err != nil {
		return nil, err
	}
	retained := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		retained[filepath.Base(filepath.Clean(root))] = struct{}{}
	}
	return retained, nil
}

// TestSwitchLaneProtectsTreeAndMetaDuringCreateWorkerBranch verifies that
// concurrent reconciliation running while CreateWorkerBranch is in flight cannot
// remove either the new branch tree or its metadata, keeps live sibling views,
// and still cleans up a genuinely stale candidate.
func TestSwitchLaneProtectsTreeAndMetaDuringCreateWorkerBranch(t *testing.T) {
	f := newRealReconcileFixture(t)
	wantViewID := laneViewID(f.workerID, 1)

	inCreate := make(chan string, 1)
	releaseCreate := make(chan struct{})
	f.ws.onCreated = func(viewID string, binding *workspace.Binding) error {
		if viewID == wantViewID {
			inCreate <- binding.Root
			<-releaseCreate
		}
		return nil
	}

	admitDone := make(chan struct{})
	var binding Binding
	var admitErr error
	go func() {
		defer close(admitDone)
		binding, admitErr = f.lanes.Admit(t.Context(), Request{
			Actor: f.actor, Op: workerrecords.Op{ID: "admit-target"}, WorkerID: f.workerID,
			Facts: Facts{Write: true}, Roots: f.roots, ActiveRootID: projectroot.SingleRootID,
		})
	}()

	createdRoot := <-inCreate
	t.Cleanup(func() {
		_ = f.mgr.DestroyWorkerWorkspace(context.Background(), &workspace.Binding{ID: wantViewID, Root: createdRoot})
	})

	// Run reconcile while CreateWorkerBranch is paused (tree + .meta exist on
	// disk, but RecordPreparation has NOT committed yet).
	snapshotTaken := make(chan struct{})
	var snapshotCalls atomic.Int32
	var settledMu sync.Mutex
	settled := map[string]int{}
	reconcileDone := make(chan struct{})
	var removed int
	var reconcileErr error
	go func() {
		defer close(reconcileDone)
		removed, reconcileErr = workspace.ReconcileStaleSandboxes(
			t.Context(), f.sandboxRoot, f.primaryDir,
			func(_ context.Context, jobID, _ string) error {
				settledMu.Lock()
				settled[jobID]++
				settledMu.Unlock()
				return nil
			},
			func(ctx context.Context) (map[string]struct{}, error) {
				res, err := f.loadRetainedBranches(ctx)
				if snapshotCalls.Add(1) == 1 {
					close(snapshotTaken)
				}
				return res, err
			},
		)
	}()

	<-snapshotTaken
	// Unblock CreateWorkerBranch so switchLane commits RecordPreparation and
	// releases the preparation guard.
	close(releaseCreate)

	<-admitDone
	<-reconcileDone
	testutil.FailErr(t, "Admit", admitErr)
	testutil.FailErr(t, "ReconcileStaleSandboxes", reconcileErr)

	if binding.Root != createdRoot || binding.Generation != 1 {
		t.Fatalf("binding = %+v, want root=%s generation=1", binding, createdRoot)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only stale candidate)", removed)
	}
	for _, kept := range []string{
		createdRoot,
		enginepaths.MetaDirForBranchRoot(createdRoot),
		f.siblingRoot,
		enginepaths.MetaDirForBranchRoot(f.siblingRoot),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected %s to survive reconcile: %v", kept, err)
		}
	}
	for _, gone := range []string{f.staleRoot, f.staleMeta} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected stale path %s to be removed, err=%v", gone, err)
		}
	}
	settledMu.Lock()
	defer settledMu.Unlock()
	if settled[wantViewID] != 0 {
		t.Fatalf("in-flight lane %s was settled %d times, want 0", wantViewID, settled[wantViewID])
	}
	if settled["stale-worker.1"] != 1 {
		t.Fatalf("stale candidate settled %d times, want 1", settled["stale-worker.1"])
	}
}

// TestSwitchLaneProtectsIntervalBetweenCreateReturnAndRecordPreparation
// exercises the exact W07-12 race window: CreateWorkerBranch has ALREADY
// returned (releasing its per-branch provision file lock), and RecordPreparation
// has NOT yet committed. Reconcile takes its initial retention snapshot during
// this interval, waits on the same owner, revalidates after RecordPreparation
// commits, and preserves the new lane while cleaning the stale candidate.
func TestSwitchLaneProtectsIntervalBetweenCreateReturnAndRecordPreparation(t *testing.T) {
	f := newRealReconcileFixture(t)
	wantViewID := laneViewID(f.workerID, 1)

	inInterval := make(chan string, 1)
	releaseInterval := make(chan struct{})
	f.lanes.afterBranchCreated = func(viewID, root string) {
		if viewID == wantViewID {
			inInterval <- root
			<-releaseInterval
		}
	}

	admitDone := make(chan struct{})
	var binding Binding
	var admitErr error
	go func() {
		defer close(admitDone)
		binding, admitErr = f.lanes.Admit(t.Context(), Request{
			Actor: f.actor, Op: workerrecords.Op{ID: "admit-target-interval"}, WorkerID: f.workerID,
			Facts: Facts{Write: true}, Roots: f.roots, ActiveRootID: projectroot.SingleRootID,
		})
	}()

	createdRoot := <-inInterval
	t.Cleanup(func() {
		_ = f.mgr.DestroyWorkerWorkspace(context.Background(), &workspace.Binding{ID: wantViewID, Root: createdRoot})
	})

	// Confirm the preparation has NOT been recorded yet.
	if _, ok, err := f.records.LatestPreparation(t.Context(), f.actor, f.workerID); err != nil {
		testutil.FailErr(t, "LatestPreparation before commit", err)
	} else if ok {
		t.Fatal("preparation should not be recorded yet in post-create interval")
	}

	snapshotTaken := make(chan struct{})
	var snapshotCalls atomic.Int32
	var settledMu sync.Mutex
	settled := map[string]int{}
	reconcileDone := make(chan struct{})
	var removed int
	var reconcileErr error
	go func() {
		defer close(reconcileDone)
		removed, reconcileErr = workspace.ReconcileStaleSandboxes(
			t.Context(), f.sandboxRoot, f.primaryDir,
			func(_ context.Context, jobID, _ string) error {
				settledMu.Lock()
				settled[jobID]++
				settledMu.Unlock()
				return nil
			},
			func(ctx context.Context) (map[string]struct{}, error) {
				res, err := f.loadRetainedBranches(ctx)
				if snapshotCalls.Add(1) == 1 {
					close(snapshotTaken)
				}
				return res, err
			},
		)
	}()

	<-snapshotTaken
	// Reconcile has seen the newly created branch on disk and taken a stale
	// retention snapshot without wantViewID; it must wait until switchLane
	// commits RecordPreparation and releases the guard.
	close(releaseInterval)

	<-admitDone
	<-reconcileDone
	testutil.FailErr(t, "Admit", admitErr)
	testutil.FailErr(t, "ReconcileStaleSandboxes", reconcileErr)

	if binding.Root != createdRoot || binding.Generation != 1 {
		t.Fatalf("binding = %+v, want root=%s generation=1", binding, createdRoot)
	}
	latest, ok, err := f.records.LatestPreparation(t.Context(), f.actor, f.workerID)
	testutil.FailErr(t, "LatestPreparation after commit", err)
	if !ok || latest.PreparationRef != createdRoot || latest.BindingGeneration != 1 {
		t.Fatalf("recorded preparation = (%+v, ok=%v), want ref=%s gen=1", latest, ok, createdRoot)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only stale candidate)", removed)
	}
	for _, kept := range []string{
		createdRoot,
		enginepaths.MetaDirForBranchRoot(createdRoot),
		f.siblingRoot,
		enginepaths.MetaDirForBranchRoot(f.siblingRoot),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected %s to survive reconcile: %v", kept, err)
		}
	}
	for _, gone := range []string{f.staleRoot, f.staleMeta} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected stale path %s to be removed, err=%v", gone, err)
		}
	}
	settledMu.Lock()
	defer settledMu.Unlock()
	if settled[wantViewID] != 0 {
		t.Fatalf("lane %s was settled %d times, want 0", wantViewID, settled[wantViewID])
	}
}

// TestSwitchLaneInterruptedPreparationIsReclaimedByReconcile verifies that
// when a lane preparation fails after creating a branch (or the worker closes
// before durable publication), releasing the guard allows reconcile to reclaim
// both the orphaned tree and its metadata while preserving live siblings.
func TestSwitchLaneInterruptedPreparationIsReclaimedByReconcile(t *testing.T) {
	f := newRealReconcileFixture(t)
	wantViewID := laneViewID(f.workerID, 1)

	inCreate := make(chan string, 1)
	releaseCreate := make(chan struct{})
	f.ws.onCreated = func(viewID string, binding *workspace.Binding) error {
		if viewID == wantViewID {
			inCreate <- binding.Root
			<-releaseCreate
			return errors.New("interrupted preparation")
		}
		return nil
	}

	admitDone := make(chan error, 1)
	go func() {
		_, err := f.lanes.Admit(t.Context(), Request{
			Actor: f.actor, Op: workerrecords.Op{ID: "admit-interrupted"}, WorkerID: f.workerID,
			Facts: Facts{Write: true}, Roots: f.roots, ActiveRootID: projectroot.SingleRootID,
		})
		admitDone <- err
	}()

	createdRoot := <-inCreate

	snapshotTaken := make(chan struct{})
	var snapshotCalls atomic.Int32
	reconcileDone := make(chan struct{})
	var removed int
	var reconcileErr error
	go func() {
		defer close(reconcileDone)
		removed, reconcileErr = workspace.ReconcileStaleSandboxes(
			t.Context(), f.sandboxRoot, f.primaryDir, nil,
			func(ctx context.Context) (map[string]struct{}, error) {
				res, err := f.loadRetainedBranches(ctx)
				if snapshotCalls.Add(1) == 1 {
					close(snapshotTaken)
				}
				return res, err
			},
		)
	}()

	<-snapshotTaken
	// Fail preparation without recording it in workerrecords.
	close(releaseCreate)

	if err := <-admitDone; err == nil {
		t.Fatal("expected interrupted Admit to fail")
	}
	<-reconcileDone
	testutil.FailErr(t, "ReconcileStaleSandboxes", reconcileErr)

	// Both the stale candidate AND the interrupted preparation should be reclaimed.
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (stale + interrupted preparation)", removed)
	}
	for _, gone := range []string{
		createdRoot,
		enginepaths.MetaDirForBranchRoot(createdRoot),
		f.staleRoot,
		f.staleMeta,
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be reclaimed, err=%v", gone, err)
		}
	}
	for _, kept := range []string{f.siblingRoot, enginepaths.MetaDirForBranchRoot(f.siblingRoot)} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected live sibling %s to survive: %v", kept, err)
		}
	}
}
