package progress_test

import (
	"strings"
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

// A new run replaces the prior goal and plan in the SQL store.
func TestEnsureBootstrap_staleRunRefreshesGoalAndPlan(t *testing.T) {
	const (
		sessionID = "sess-root"
		priorRun  = "run-prior"
		newRun    = "run-survey"
	)
	store := newBootstrapStore(t, sessionID, priorRun, newRun)
	if _, err := store.Update(t.Context(), sessionID, func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{RunID: priorRun, Content: "# Goal\n\n" + "Implement auth middleware for the API" + "\n\n## Progress\n"}, nil
	}); err != nil {
		t.Fatalf("seed progress run: %v", err)
	}
	testutil.FailErr(t, "seed prior plan", store.Set(sessionID, "# Goal\n\nImplement auth middleware for the API\n\n## Progress\n- [x] stale step\n"))

	if refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, newRun, "Survey this repo and produce a research brief"); err != nil || !refreshed {
		t.Fatal("expected stale-run refresh")
	}
	got := store.Get(t.Context(), sessionID)
	if !strings.Contains(got, "Survey this repo and produce a research brief") {
		t.Fatalf("Goal not refreshed: %q", got)
	}
	if strings.Contains(got, "stale step") {
		t.Fatalf("Plan not reset: %q", got)
	}
	if !progress.ProgressMissing(got) {
		t.Fatalf("expected empty ## Progress after refresh, got %q", got)
	}
	if boundRun(t, store, sessionID) != newRun {
		t.Fatalf("run id = %q want %q", boundRun(t, store, sessionID), newRun)
	}
}

func TestEnsureBootstrap_sameRunOpenPlanNoOp(t *testing.T) {
	const (
		sessionID = "sess-same"
		runID     = "run-1"
	)
	store := newBootstrapStore(t, sessionID, runID)
	before := "# Goal\n\nKeep me\n\n## Progress\n- [ ] step"
	if _, err := store.Update(t.Context(), sessionID, func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{RunID: runID, Content: "# Goal\n\n" + "Keep me" + "\n\n## Progress\n"}, nil
	}); err != nil {
		t.Fatalf("seed progress run: %v", err)
	}
	testutil.FailErr(t, "seed plan", store.Set(sessionID, before))

	if refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, runID, "Different prompt text"); err != nil || refreshed {
		t.Fatal("same run with an open plan must not refresh")
	}
	if got := store.Get(t.Context(), sessionID); got != before {
		t.Fatalf("content changed on same run:\nbefore=%q\ngot=%q", before, got)
	}
}

func TestEnsureBootstrap_sameRunTerminalPlanStartsFreshRun(t *testing.T) {
	const (
		sessionID = "sess-ambient"
		runID     = "run-ambient"
	)
	store := newBootstrapStore(t, sessionID, runID)
	// The ambient implement@ run keeps the same id across turns. A finished plan from the
	// prior turn must not carry into the next task.
	if _, err := store.Update(t.Context(), sessionID, func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{RunID: runID, Content: "# Goal\n\n" + "Implement the first task" + "\n\n## Progress\n"}, nil
	}); err != nil {
		t.Fatalf("seed progress run: %v", err)
	}
	testutil.FailErr(t, "seed terminal plan", store.Set(sessionID, "# Goal\n\nImplement the first task\n\n## Progress\n- [x] done step\n- [~] skipped step\n"))

	if refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, runID, "Now do the second task"); err != nil || !refreshed {
		t.Fatal("same run with a fully terminal plan must refresh for the new turn")
	}
	got := store.Get(t.Context(), sessionID)
	if !strings.Contains(got, "Now do the second task") {
		t.Fatalf("Goal not refreshed: %q", got)
	}
	if strings.Contains(got, "done step") || strings.Contains(got, "skipped step") {
		t.Fatalf("prior run's terminal steps survived reset: %q", got)
	}
	if !progress.ProgressMissing(got) {
		t.Fatalf("expected empty ## Progress after refresh, got %q", got)
	}
	if boundRun(t, store, sessionID) != runID {
		t.Fatalf("run id = %q want %q", boundRun(t, store, sessionID), runID)
	}
}

func TestEnsureBootstrap_sameRunEmptyPlanNoOp(t *testing.T) {
	const (
		sessionID = "sess-empty"
		runID     = "run-empty"
	)
	store := newBootstrapStore(t, sessionID, runID)
	// A turn that authored no checklist is not a finished run — AllTerminal is false with no
	// items, so a follow-up turn under the same run must not spuriously refresh.
	if _, err := store.Update(t.Context(), sessionID, func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{RunID: runID, Content: "# Goal\n\n" + "Answer a question" + "\n\n## Progress\n"}, nil
	}); err != nil {
		t.Fatalf("seed progress run: %v", err)
	}
	before := store.Get(t.Context(), sessionID)

	if refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, runID, "Answer another question"); err != nil || refreshed {
		t.Fatal("same run with no checklist must not refresh")
	}
	if got := store.Get(t.Context(), sessionID); got != before {
		t.Fatalf("content changed on same run:\nbefore=%q\ngot=%q", before, got)
	}
}

func TestAdoptActiveRun_unboundBindsWithoutWipe(t *testing.T) {
	const (
		sessionID = "sess-adopt"
		runID     = "run-child"
	)
	database := testdbfixture.Open(t, "progress.db")
	testdbseed.InsertWorkflowRun(t, database, runID, sessionID, testdbseed.DefaultProjectID)
	store := progress.NewSQLStore(database)
	seeded := "## Progress\n- [ ] keep me"
	_, err := database.ExecContext(t.Context(), `INSERT INTO session_progress(session_id,workflow_run_id,content,item_ids_json,document_version,updated_at) VALUES(?, '', ?, '["id-1"]', 1, '2026-01-01T00:00:00Z')`, sessionID, seeded)
	testutil.FailErr(t, "seed unbound progress", err)

	if refreshed, err := progress.AdoptActiveRun(t.Context(), store, sessionID, runID, "ignored"); err != nil || refreshed {
		t.Fatal("first adopt must not refresh content")
	}
	if got := store.Get(t.Context(), sessionID); got != seeded {
		t.Fatalf("content wiped on first adopt: got %q want %q", got, seeded)
	}
	if boundRun(t, store, sessionID) != runID {
		t.Fatalf("run id = %q want %q", boundRun(t, store, sessionID), runID)
	}
}

func TestAdoptActiveRun_distinctRunResets(t *testing.T) {
	const (
		sessionID = "sess-handoff"
		priorRun  = "run-plan"
		childRun  = "run-implement"
	)
	store := newBootstrapStore(t, sessionID, priorRun, childRun)
	if _, err := store.Update(t.Context(), sessionID, func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{RunID: priorRun, Content: "# Goal\n\n" + "Plan the feature" + "\n\n## Progress\n"}, nil
	}); err != nil {
		t.Fatalf("seed progress run: %v", err)
	}
	testutil.FailErr(t, "seed plan", store.Set(sessionID, "# Goal\n\nPlan the feature\n\n## Progress\n- [x] draft blueprint\n"))

	if refreshed, err := progress.AdoptActiveRun(t.Context(), store, sessionID, childRun, "Build from blueprint"); err != nil || !refreshed {
		t.Fatal("distinct run must refresh")
	}
	got := store.Get(t.Context(), sessionID)
	if strings.Contains(got, "draft blueprint") {
		t.Fatalf("prior checklist survived handoff: %q", got)
	}
	if !strings.Contains(got, "Build from blueprint") {
		t.Fatalf("Goal not refreshed: %q", got)
	}
	if boundRun(t, store, sessionID) != childRun {
		t.Fatalf("run id = %q want %q", boundRun(t, store, sessionID), childRun)
	}
}

func TestAdoptActiveRun_sameRunNoOp(t *testing.T) {
	const (
		sessionID = "sess-same-adopt"
		runID     = "run-1"
	)
	store := newBootstrapStore(t, sessionID, runID)
	if _, err := store.Update(t.Context(), sessionID, func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{RunID: runID, Content: "# Goal\n\n" + "Keep" + "\n\n## Progress\n"}, nil
	}); err != nil {
		t.Fatalf("seed progress run: %v", err)
	}
	testutil.FailErr(t, "seed plan", store.Set(sessionID, "# Goal\n\nKeep\n\n## Progress\n- [x] done\n"))
	before := store.Get(t.Context(), sessionID)

	if refreshed, err := progress.AdoptActiveRun(t.Context(), store, sessionID, runID, "New goal ignored"); err != nil || refreshed {
		t.Fatal("same run must not refresh on phase re-entry")
	}
	if got := store.Get(t.Context(), sessionID); got != before {
		t.Fatalf("content changed on same run:\nbefore=%q\ngot=%q", before, got)
	}
}

func TestEnsureBootstrap_unboundDocBindsWithoutWipe(t *testing.T) {
	const (
		sessionID = "sess-bind"
		runID     = "run-first"
	)
	database := testdbfixture.Open(t, "progress.db")
	testdbseed.InsertWorkflowRun(t, database, runID, sessionID, testdbseed.DefaultProjectID)
	store := progress.NewSQLStore(database)
	seeded := "## Progress\n- [ ] resolve TODOs"
	_, err := database.ExecContext(t.Context(), `INSERT INTO session_progress(session_id,workflow_run_id,content,item_ids_json,document_version,updated_at) VALUES(?, '', ?, '["id-1"]', 1, '2026-01-01T00:00:00Z')`, sessionID, seeded)
	testutil.FailErr(t, "seed unbound progress", err)

	if refreshed, err := progress.EnsureBootstrap(t.Context(), store, sessionID, runID, "Build the stub"); err != nil || refreshed {
		t.Fatal("first bind must not refresh content")
	}
	if got := store.Get(t.Context(), sessionID); got != seeded {
		t.Fatalf("content wiped on first bind: got %q want %q", got, seeded)
	}
	if boundRun(t, store, sessionID) != runID {
		t.Fatalf("run id = %q want %q", boundRun(t, store, sessionID), runID)
	}
}

func TestEnsureBootstrap_setPreservesBoundRun(t *testing.T) {
	const (
		sessionID = "sess-preserve"
		runID     = "run-bound"
	)
	store := newBootstrapStore(t, sessionID, runID)
	if _, err := store.Update(t.Context(), sessionID, func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{RunID: runID, Content: "# Goal\n\n" + "Goal text" + "\n\n## Progress\n"}, nil
	}); err != nil {
		t.Fatalf("seed progress run: %v", err)
	}
	testutil.FailErr(t, "set plan", store.Set(sessionID, "# Goal\n\nGoal text\n\n## Progress\n- [ ] authored\n"))
	if boundRun(t, store, sessionID) != runID {
		t.Fatalf("bound run after Set = %q want %q", boundRun(t, store, sessionID), runID)
	}
}

func boundRun(t *testing.T, store progress.Store, sessionID string) string {
	t.Helper()
	snapshot, err := store.Snapshot(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("read bound run: %v", err)
	}
	return snapshot.RunID
}
