package progress_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func newBootstrapStore(t *testing.T, sessionID string, runIDs ...string) *progress.SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "progress.db")
	for i, runID := range runIDs {
		testdbseed.InsertWorkflowRun(t, database, runID, sessionID, testdbseed.DefaultProjectID)
		if i < len(runIDs)-1 {
			_, err := database.ExecContext(t.Context(), `UPDATE workflow_runs SET status = 'complete', completed_at = updated_at WHERE id = ?`, runID)
			testutil.FailErr(t, "mark prior run complete", err)
		}
	}
	return progress.NewSQLStore(database)
}

func TestEnsureBootstrapStaleRunResetsItemsAndPreservesSeq(t *testing.T) {
	const (
		sessionID = "sess-root"
		priorRun  = "run-prior"
		newRun    = "run-survey"
	)
	store := newBootstrapStore(t, sessionID, priorRun, newRun)
	_, err := store.Apply(t.Context(), sessionID, progress.Mutation{
		Replace: []progress.RowSpec{{Label: "stale step", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{RunID: priorRun})
	testutil.FailErr(t, "seed prior plan", err)
	done := progress.ProgressStateDone
	_, err = store.Apply(t.Context(), sessionID, progress.Mutation{
		Update: []progress.UpdateRowSpec{{ID: "p1", State: &done}},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "complete prior plan", err)

	refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, newRun)
	if err != nil || !refreshed {
		t.Fatalf("expected stale-run refresh: refreshed=%v err=%v", refreshed, err)
	}
	snap, err := store.Snapshot(t.Context(), sessionID)
	testutil.FailErr(t, "read refreshed snapshot", err)
	if !snap.Missing() || snap.RunID != newRun || snap.NextItemSeq() != 2 {
		t.Fatalf("unexpected refreshed snapshot: %+v seq=%d", snap, snap.NextItemSeq())
	}
}

func TestEnsureBootstrapSameRunOpenPlanNoOp(t *testing.T) {
	const (
		sessionID = "sess-same"
		runID     = "run-1"
	)
	store := newBootstrapStore(t, sessionID, runID)
	seeded, err := store.Apply(t.Context(), sessionID, progress.Mutation{
		Replace: []progress.RowSpec{{Label: "step", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{RunID: runID})
	testutil.FailErr(t, "seed open plan", err)

	refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, runID)
	if err != nil || refreshed {
		t.Fatalf("same run with open plan must not refresh: refreshed=%v err=%v", refreshed, err)
	}
	got, err := store.Snapshot(t.Context(), sessionID)
	testutil.FailErr(t, "read snapshot", err)
	if !got.Equal(seeded.Snapshot) {
		t.Fatalf("snapshot changed on same run: %+v != %+v", got, seeded.Snapshot)
	}
}

func TestEnsureBootstrapSameRunTerminalPlanResets(t *testing.T) {
	const (
		sessionID = "sess-ambient"
		runID     = "run-ambient"
	)
	store := newBootstrapStore(t, sessionID, runID)
	_, err := store.Apply(t.Context(), sessionID, progress.Mutation{
		Replace: []progress.RowSpec{{Label: "open", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{RunID: runID})
	testutil.FailErr(t, "seed open plan", err)
	done := progress.ProgressStateDone
	_, err = store.Apply(t.Context(), sessionID, progress.Mutation{
		Update: []progress.UpdateRowSpec{{ID: "p1", State: &done}},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "complete plan", err)

	refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, runID)
	if err != nil || !refreshed {
		t.Fatalf("terminal plan on same run must refresh: refreshed=%v err=%v", refreshed, err)
	}
	got, err := store.Snapshot(t.Context(), sessionID)
	testutil.FailErr(t, "read reset snapshot", err)
	if !got.Missing() || got.RunID != runID || got.NextItemSeq() != 2 {
		t.Fatalf("reset snapshot = %+v seq=%d", got, got.NextItemSeq())
	}
}

func TestEnsureBootstrapSameRunEmptyPlanNoOp(t *testing.T) {
	const (
		sessionID = "sess-empty"
		runID     = "run-empty"
	)
	store := newBootstrapStore(t, sessionID, runID)
	_, err := progress.EnsureBootstrap(t.Context(), store, sessionID, runID)
	testutil.FailErr(t, "initial bind", err)
	before, err := store.Snapshot(t.Context(), sessionID)
	testutil.FailErr(t, "read bound empty plan", err)

	refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, runID)
	if err != nil || refreshed {
		t.Fatalf("empty plan on same run must not refresh: refreshed=%v err=%v", refreshed, err)
	}
	after, err := store.Snapshot(t.Context(), sessionID)
	testutil.FailErr(t, "read after second bootstrap", err)
	if !before.Equal(after) {
		t.Fatalf("empty plan mutated: %+v != %+v", before, after)
	}
}

func TestAdoptActiveRunBindsUnboundAndResetsDistinctRun(t *testing.T) {
	const (
		sessionID = "sess-handoff"
		priorRun  = "run-plan"
		childRun  = "run-implement"
	)
	store := newBootstrapStore(t, sessionID, priorRun, childRun)
	_, err := store.Apply(t.Context(), sessionID, progress.Mutation{
		Replace: []progress.RowSpec{{Label: "draft blueprint", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{RunID: priorRun})
	testutil.FailErr(t, "seed prior run plan", err)

	if refreshed, err := progress.AdoptActiveRun(t.Context(), store, sessionID, priorRun); err != nil || refreshed {
		t.Fatalf("same run adopt must no-op: refreshed=%v err=%v", refreshed, err)
	}
	if refreshed, err := progress.AdoptActiveRun(t.Context(), store, sessionID, childRun); err != nil || !refreshed {
		t.Fatalf("distinct run adopt must reset: refreshed=%v err=%v", refreshed, err)
	}
	snap, err := store.Snapshot(t.Context(), sessionID)
	testutil.FailErr(t, "read adopted snapshot", err)
	if !snap.Missing() || snap.RunID != childRun || snap.NextItemSeq() != 2 {
		t.Fatalf("adopted snapshot = %+v seq=%d", snap, snap.NextItemSeq())
	}
}
