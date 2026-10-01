package progress_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
)

func TestEnsureBootstrap_staleRunRefreshesGoalAndPlan(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-root"
		priorRun  = "run-prior"
		newRun    = "run-survey"
	)
	store.EnsureRun(sessionID, priorRun, "Implement auth middleware for the API")
	store.Set(sessionID, "# Goal\n\nImplement auth middleware for the API\n\n## Progress\n- [x] stale step\n")

	if refreshed := progress.EnsureBootstrap(t.Context(), store, sessionID, newRun, "Survey this repo and produce a research brief"); !refreshed {
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
	if store.BoundRunID(sessionID) != newRun {
		t.Fatalf("run id = %q want %q", store.BoundRunID(sessionID), newRun)
	}
}

func TestEnsureBootstrap_sameRunOpenPlanNoOp(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-same"
		runID     = "run-1"
	)
	before := "# Goal\n\nKeep me\n\n## Progress\n- [ ] step"
	store.EnsureRun(sessionID, runID, "Keep me")
	store.Set(sessionID, before)

	if refreshed := progress.EnsureBootstrap(t.Context(), store, sessionID, runID, "Different prompt text"); refreshed {
		t.Fatal("same run with an open plan must not refresh")
	}
	if got := store.Get(t.Context(), sessionID); got != before {
		t.Fatalf("content changed on same run:\nbefore=%q\ngot=%q", before, got)
	}
}

func TestEnsureBootstrap_sameRunTerminalPlanStartsFreshRun(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-ambient"
		runID     = "run-ambient"
	)
	// The ambient implement@ run keeps the same id across turns. A finished plan from the
	// prior turn must not carry into the next task.
	store.EnsureRun(sessionID, runID, "Implement the first task")
	store.Set(sessionID, "# Goal\n\nImplement the first task\n\n## Progress\n- [x] done step\n- [~] skipped step\n")

	if refreshed := progress.EnsureBootstrap(t.Context(), store, sessionID, runID, "Now do the second task"); !refreshed {
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
	if store.BoundRunID(sessionID) != runID {
		t.Fatalf("run id = %q want %q", store.BoundRunID(sessionID), runID)
	}
}

func TestEnsureBootstrap_sameRunEmptyPlanNoOp(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-empty"
		runID     = "run-empty"
	)
	// A turn that authored no checklist is not a finished run — AllTerminal is false with no
	// items, so a follow-up turn under the same run must not spuriously refresh.
	store.EnsureRun(sessionID, runID, "Answer a question")
	before := store.Get(t.Context(), sessionID)

	if refreshed := progress.EnsureBootstrap(t.Context(), store, sessionID, runID, "Answer another question"); refreshed {
		t.Fatal("same run with no checklist must not refresh")
	}
	if got := store.Get(t.Context(), sessionID); got != before {
		t.Fatalf("content changed on same run:\nbefore=%q\ngot=%q", before, got)
	}
}

func TestAdoptActiveRun_unboundBindsWithoutWipe(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-adopt"
		runID     = "run-child"
	)
	seeded := "## Progress\n- [ ] keep me"
	store.Set(sessionID, seeded)

	if refreshed := progress.AdoptActiveRun(t.Context(), store, sessionID, runID, "ignored"); refreshed {
		t.Fatal("first adopt must not refresh content")
	}
	if got := store.Get(t.Context(), sessionID); got != seeded {
		t.Fatalf("content wiped on first adopt: got %q want %q", got, seeded)
	}
	if store.BoundRunID(sessionID) != runID {
		t.Fatalf("run id = %q want %q", store.BoundRunID(sessionID), runID)
	}
}

func TestAdoptActiveRun_distinctRunResets(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-handoff"
		priorRun  = "run-plan"
		childRun  = "run-implement"
	)
	store.EnsureRun(sessionID, priorRun, "Plan the feature")
	store.Set(sessionID, "# Goal\n\nPlan the feature\n\n## Progress\n- [x] draft blueprint\n")

	if refreshed := progress.AdoptActiveRun(t.Context(), store, sessionID, childRun, "Build from blueprint"); !refreshed {
		t.Fatal("distinct run must refresh")
	}
	got := store.Get(t.Context(), sessionID)
	if strings.Contains(got, "draft blueprint") {
		t.Fatalf("prior checklist survived handoff: %q", got)
	}
	if !strings.Contains(got, "Build from blueprint") {
		t.Fatalf("Goal not refreshed: %q", got)
	}
	if store.BoundRunID(sessionID) != childRun {
		t.Fatalf("run id = %q want %q", store.BoundRunID(sessionID), childRun)
	}
}

func TestAdoptActiveRun_sameRunNoOp(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-same-adopt"
		runID     = "run-1"
	)
	store.EnsureRun(sessionID, runID, "Keep")
	store.Set(sessionID, "# Goal\n\nKeep\n\n## Progress\n- [x] done\n")
	before := store.Get(t.Context(), sessionID)

	if refreshed := progress.AdoptActiveRun(t.Context(), store, sessionID, runID, "New goal ignored"); refreshed {
		t.Fatal("same run must not refresh on phase re-entry")
	}
	if got := store.Get(t.Context(), sessionID); got != before {
		t.Fatalf("content changed on same run:\nbefore=%q\ngot=%q", before, got)
	}
}

func TestEnsureBootstrap_unboundDocBindsWithoutWipe(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-bind"
		runID     = "run-first"
	)
	seeded := "## Progress\n- [ ] resolve TODOs"
	store.Set(sessionID, seeded)

	if refreshed := progress.EnsureBootstrap(t.Context(), store, sessionID, runID, "Build the stub"); refreshed {
		t.Fatal("first bind must not refresh content")
	}
	if got := store.Get(t.Context(), sessionID); got != seeded {
		t.Fatalf("content wiped on first bind: got %q want %q", got, seeded)
	}
	if store.BoundRunID(sessionID) != runID {
		t.Fatalf("run id = %q want %q", store.BoundRunID(sessionID), runID)
	}
}

func TestEnsureBootstrap_setPreservesBoundRun(t *testing.T) {
	store := progress.NewMemoryStore()
	const (
		sessionID = "sess-preserve"
		runID     = "run-bound"
	)
	store.EnsureRun(sessionID, runID, "Goal text")
	store.Set(sessionID, "# Goal\n\nGoal text\n\n## Progress\n- [ ] authored\n")
	if store.BoundRunID(sessionID) != runID {
		t.Fatalf("bound run after Set = %q want %q", store.BoundRunID(sessionID), runID)
	}
}
