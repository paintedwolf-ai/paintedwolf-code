package workflow

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"sync"
	"testing"
)

// A revision bumped between the caller's load and its commit is not a conflict:
// the stamp re-reads and re-applies, so the write lands on the newer revision.
func TestStampRunVarsAbsorbsRevisionBumpAfterCallerLoad(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	// The snapshot a background writer would have been holding.
	stale := *run

	// Somebody else writes first, so `stale` is now behind by one revision.
	if _, err := mgr.Phases.Vars.Stamp(ctx, run.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		return runstate.SetHostVar(vars, "probe.first", "yes"), true, nil
	}); err != nil {
		testutil.FailErr(t, "first stamp", err)
	}
	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if after.Revision <= stale.Revision {
		t.Fatalf("first stamp did not bump revision: %d then %d", stale.Revision, after.Revision)
	}

	// The stamp derives from the run it is handed, never from the stale one.
	seen := int64(0)
	stamped, err := mgr.Phases.Vars.Stamp(ctx, run.ID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		seen = run.Revision
		return runstate.SetHostVar(vars, "probe.second", "yes"), true, nil
	})
	testutil.FailErr(t, "second stamp", err)
	if seen != after.Revision {
		t.Fatalf("mutation saw revision %d want the freshly loaded %d", seen, after.Revision)
	}
	if stamped == nil {
		t.Fatal("stamp returned no run")
	}

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if !conditions.DotPathTruthy(vars, "probe.first") || !conditions.DotPathTruthy(vars, "probe.second") {
		t.Fatalf("both stamps must survive, got %#v", vars["probe"])
	}
}

// Several background writers stamping one run concurrently: every mutation must
// land, and none may report a revision conflict to its caller.
func TestStampRunVarsConcurrentWritersAllLand(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	const writers = 6
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "probe.w" + string(rune('a'+i))
			_, errs[i] = mgr.Phases.Vars.Stamp(ctx, run.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
				return runstate.SetHostVar(vars, key, "yes"), true, nil
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
		if errors.Is(err, runstate.ErrRevisionConflict) {
			t.Fatalf("writer %d surfaced a revision conflict", i)
		}
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	for i := 0; i < writers; i++ {
		key := "probe.w" + string(rune('a'+i))
		if !conditions.DotPathTruthy(vars, key) {
			t.Fatalf("%s was lost: %#v", key, vars["probe"])
		}
	}
}

// A mutation that declines writes nothing and still reports the run, so callers
// can tell "nothing to do" from "no such run".
func TestStampRunVarsDeclinedMutationDoesNotWrite(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	before, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)

	stamped, err := mgr.Phases.Vars.Stamp(ctx, run.ID, func(context.Context, *api.WorkflowRun, map[string]any) (map[string]any, bool, error) {
		return nil, false, nil
	})
	testutil.FailErr(t, "declined stamp", err)
	if stamped == nil {
		t.Fatal("declined stamp must still return the loaded run")
	}
	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after", err)
	if after.Revision != before.Revision {
		t.Fatalf("declined stamp wrote: revision %d then %d", before.Revision, after.Revision)
	}
}

// A mutation error is the caller's contract, not a lost update: it comes back
// unwrapped and unretried.
func TestStampRunVarsMutationErrorIsNotRetried(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	sentinel := errors.New("mutation refused")
	attempts := 0
	if _, err := mgr.Phases.Vars.Stamp(ctx, run.ID, func(context.Context, *api.WorkflowRun, map[string]any) (map[string]any, bool, error) {
		attempts++
		return nil, false, sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v want %v", err, sentinel)
	}
	if attempts != 1 {
		t.Fatalf("mutation ran %d times, want 1", attempts)
	}
}

// An unknown run is absence, not an error, so background stamps for a run that
// was canceled underneath them stay quiet.
func TestStampRunVarsUnknownRunIsAbsence(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	stamped, err := mgr.Phases.Vars.Stamp(context.Background(), "no-such-run", func(context.Context, *api.WorkflowRun, map[string]any) (map[string]any, bool, error) {
		t.Fatal("mutation must not run for an unknown run")
		return nil, false, nil
	})
	testutil.FailErr(t, "unknown run stamp", err)
	if stamped != nil {
		t.Fatalf("stamped = %#v want nil", stamped)
	}
}
