package workerlane_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
)

type createCall struct{ viewID, parent string }

// fakeWorkspaces records branch preparation and eviction.
type fakeWorkspaces struct {
	mu       sync.Mutex
	base     string
	created  []createCall
	evicted  []string
	failNext error
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (f *fakeWorkspaces) create(viewID, parent string) (*workspace.Binding, workspace.SandboxLayout, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		seen := f.maxSeen.Load()
		if n <= seen || f.maxSeen.CompareAndSwap(seen, n) {
			break
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return nil, workspace.SandboxLayout{}, err
	}
	f.created = append(f.created, createCall{viewID: viewID, parent: parent})
	return &workspace.Binding{Root: filepath.Join(f.base, viewID)}, workspace.SandboxLayout{}, nil
}

func (f *fakeWorkspaces) CreateWorkerBranch(_ context.Context, _ []projectroot.RootRef, _, viewID string) (*workspace.Binding, workspace.SandboxLayout, error) {
	return f.create(viewID, "")
}

func (f *fakeWorkspaces) CreateStackedWorkerBranch(_ context.Context, _ []projectroot.RootRef, _, viewID, parent string) (*workspace.Binding, workspace.SandboxLayout, error) {
	return f.create(viewID, parent)
}

func (f *fakeWorkspaces) EvictWorkerBranch(_ context.Context, root string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, root)
	return nil
}

type laneFixture struct {
	ws       *fakeWorkspaces
	lanes    *workerlane.Service
	actor    workerrecords.Actor
	workerID string
	ops      atomic.Int64
}

func newLaneFixture(t *testing.T) *laneFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "lanes.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	records := workerrecords.New(sqlDB)
	actor := workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}
	w, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite})
	testutil.FailErr(t, "create worker", err)
	ws := &fakeWorkspaces{base: t.TempDir()}
	return &laneFixture{ws: ws, lanes: workerlane.New(ws, records), actor: actor, workerID: w.WorkerID}
}

func (f *laneFixture) admit(t *testing.T, facts workerlane.Facts) (workerlane.Binding, error) {
	t.Helper()
	return f.lanes.Admit(t.Context(), workerlane.Request{
		Actor: f.actor, Op: workerrecords.Op{ID: "admit-" + strconv.FormatInt(f.ops.Add(1), 10)}, WorkerID: f.workerID, Facts: facts,
	})
}

func TestLaneIsKeptForCorrectionsAndSwitchedOnLandingAndRejection(t *testing.T) {
	f := newLaneFixture(t)
	first, err := f.admit(t, workerlane.Facts{Write: true})
	testutil.FailErr(t, "first round", err)
	if first.Generation != 1 || first.Action != workerlane.ActionPrepare || f.ws.created[0].viewID != f.workerID+".1" {
		t.Fatalf("first binding = %+v, created %+v", first, f.ws.created)
	}
	kept, err := f.admit(t, workerlane.Facts{Write: true, HasLane: true})
	testutil.FailErr(t, "correction round", err)
	if kept.Root != first.Root || kept.Generation != 1 || len(f.ws.created) != 1 {
		t.Fatalf("correction did not keep the lane: %+v", kept)
	}

	refreshed, err := f.admit(t, workerlane.Facts{Write: true, HasLane: true, LastChange: workerlane.ChangeLanded})
	testutil.FailErr(t, "after landing", err)
	if refreshed.Generation != 2 || refreshed.Root == first.Root || f.ws.created[1].parent != "" {
		t.Fatalf("landing did not refresh from source: %+v %+v", refreshed, f.ws.created)
	}
	if len(f.ws.evicted) != 1 || f.ws.evicted[0] != first.Root {
		t.Fatalf("evicted = %v, want the first generation", f.ws.evicted)
	}

	reset, err := f.admit(t, workerlane.Facts{Write: true, HasLane: true, LastChange: workerlane.ChangeRejected, PriorCheckpoint: "cp-prior"})
	testutil.FailErr(t, "after rejection", err)
	if reset.Generation != 3 || f.ws.created[2].parent != "cp-prior" {
		t.Fatalf("rejection did not reset to the prior checkpoint: %+v %+v", reset, f.ws.created)
	}
}

// A failed preparation records nothing: the lane stays on its last binding.
func TestLaneSwitchesOnlyWhenPreparationSucceeds(t *testing.T) {
	f := newLaneFixture(t)
	first, err := f.admit(t, workerlane.Facts{Write: true})
	testutil.FailErr(t, "first round", err)
	f.ws.failNext = errors.New("disk full")
	_, err = f.admit(t, workerlane.Facts{Write: true, HasLane: true, LastChange: workerlane.ChangeLanded})
	if err == nil {
		t.Fatal("failed preparation admitted")
	}
	if len(f.ws.evicted) != 0 {
		t.Fatalf("old lane evicted after a failed preparation: %v", f.ws.evicted)
	}
	kept, err := f.admit(t, workerlane.Facts{Write: true, HasLane: true})
	testutil.FailErr(t, "lane after failure", err)
	if kept.Root != first.Root || kept.Generation != 1 {
		t.Fatalf("binding moved without a prepared view: %+v", kept)
	}
	// Only preparation is retried.
	retried, err := f.admit(t, workerlane.Facts{Write: true, HasLane: true, LastChange: workerlane.ChangeLanded})
	testutil.FailErr(t, "retry preparation", err)
	if retried.Generation != 2 {
		t.Fatalf("retry = %+v", retried)
	}
}

func TestLaneHoldsAndReadRoundsTouchNoWorkspace(t *testing.T) {
	f := newLaneFixture(t)
	_, err := f.admit(t, workerlane.Facts{Write: true, HasLane: true, RecoveryRequired: true})
	var hold *workerlane.HoldError
	if !errors.As(err, &hold) || hold.Reason != workerlane.HoldRecovery || !errors.Is(err, workerlane.ErrHeld) {
		t.Fatalf("hold = %v", err)
	}
	_, err = f.admit(t, workerlane.Facts{Write: true, HasLane: true})
	if !errors.Is(err, workerlane.ErrNotPrepared) {
		t.Fatalf("keep without a lane = %v", err)
	}
	read, err := f.admit(t, workerlane.Facts{})
	testutil.FailErr(t, "read round", err)
	if read.Root != "" || read.Action != workerlane.ActionSource || len(f.ws.created) != 0 {
		t.Fatalf("read round = %+v, created %v", read, f.ws.created)
	}
}

// One worker's lane operations run one at a time.
func TestLaneOperationsSerializePerWorker(t *testing.T) {
	f := newLaneFixture(t)
	_, err := f.admit(t, workerlane.Facts{Write: true})
	testutil.FailErr(t, "first round", err)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.lanes.Admit(context.Background(), workerlane.Request{
				Actor: f.actor, Op: workerrecords.Op{ID: "race-" + strconv.FormatInt(int64(i), 10)}, WorkerID: f.workerID,
				Facts: workerlane.Facts{Write: true, HasLane: true, LastChange: workerlane.ChangeLanded},
			})
			if err != nil {
				t.Errorf("admit: %v", err)
			}
		}()
	}
	wg.Wait()
	if f.ws.maxSeen.Load() != 1 {
		t.Fatalf("concurrent preparations for one worker = %d", f.ws.maxSeen.Load())
	}
	if n := len(f.ws.created); n != 9 {
		t.Fatalf("preparations = %d, want one per admission", n)
	}
}

// A reviewer's lane is a view of its sealed subject, prepared once and kept
// across its rounds.
func TestReviewerLaneIsAViewOfItsSubject(t *testing.T) {
	f := newLaneFixture(t)
	admit := func(op string) (workerlane.Binding, error) {
		return f.lanes.Admit(t.Context(), workerlane.Request{
			Actor: f.actor, Op: workerrecords.Op{ID: op}, WorkerID: f.workerID,
			Facts: workerlane.Facts{Reviewer: true, SubjectCheckpoint: "cp-subject"},
		})
	}
	first, err := admit("review-1")
	testutil.FailErr(t, "first review round", err)
	if first.Action != workerlane.ActionSubject || first.Root == "" || len(f.ws.created) != 1 || f.ws.created[0].parent != "cp-subject" {
		t.Fatalf("review view = %+v, created %+v", first, f.ws.created)
	}
	again, err := admit("review-2")
	testutil.FailErr(t, "second review round", err)
	if again.Root != first.Root || len(f.ws.created) != 1 || len(f.ws.evicted) != 0 {
		t.Fatalf("second round moved the view: %+v, created %+v", again, f.ws.created)
	}
}
