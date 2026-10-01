package assembly

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRunContextFingerprintChangesOnPhase(t *testing.T) {
	base := inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{
		WorkflowID: "wf-1", RunID: "run-1", CoordinatorBrief: "brief",
	}})
	a := inject.ActiveWorkflowInjectFingerprint(base, nil)
	changed := base
	changed.CurrentPhase = "implement"
	b := inject.ActiveWorkflowInjectFingerprint(changed, nil)
	if a == b {
		t.Fatalf("phase change should alter fingerprint: %q", a)
	}
}

func TestRunContextFingerprintIncludesPendingFeedback(t *testing.T) {
	ctx := api.CoordinatorRunContext{WorkflowID: "wf-1"}
	base := inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{RunContext: ctx})
	a := inject.ActiveWorkflowInjectFingerprint(base, nil)
	ctx.PendingFeedback = &api.PendingFeedback{PhaseID: "plan", Prompt: "choose"}
	updated := inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{RunContext: ctx})
	b := inject.ActiveWorkflowInjectFingerprint(updated, nil)
	if a == b {
		t.Fatal("pending feedback should alter fingerprint")
	}
}

func TestOmitRunContextOnIterationTwo(t *testing.T) {
	engine := &AssemblyEngine{}
	turn := &TurnAssemblyScratch{Iteration: 1}
	injectData := inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{WorkflowID: "wf-1", CoordinatorBrief: "stay"}})
	hints := []string{"WORKFLOW_GATE_UNMET"}
	block := "cached-block"
	key := inject.ActiveWorkflowInjectFingerprint(injectData, hints)
	turn.RunContextKey = key
	turn.RunContextBlock = block

	if engine.shouldInjectRunContext(turn, injectData, hints, block) {
		t.Fatal("expected run context omitted when fingerprint stable on iteration 1+")
	}
}

func TestBoardAndRunContextIndependent(t *testing.T) {
	runA := inject.ActiveWorkflowInjectFingerprint(inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{WorkflowID: "a"}}), nil)
	runB := inject.ActiveWorkflowInjectFingerprint(inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{WorkflowID: "b"}}), nil)
	boardA := boardInjectFingerprint("hash-a", "phase-a")
	boardB := boardInjectFingerprint("hash-b", "phase-b")
	if runA == runB {
		t.Fatal("run context fingerprints should differ")
	}
	if boardA == boardB {
		t.Fatal("board fingerprints should differ")
	}
}

func TestSessionPromptCacheBeginEndTurn(t *testing.T) {
	var cache SessionPromptCache
	cache.BeginTurn("sess-1", "kick-a")
	turn := cache.LoadTurn("sess-1")
	if len(turn.PendingKickIDs) != 1 || turn.PendingKickIDs[0] != "kick-a" {
		t.Fatalf("PendingKickIDs = %v", turn.PendingKickIDs)
	}
	turn.Iteration = 7
	cache.EndTurn("sess-1")
	if next := cache.LoadTurn("sess-1"); next == turn || next.Iteration != 0 || len(next.PendingKickIDs) != 0 {
		t.Fatalf("expected fresh turn after end, got %+v", next)
	}
}

func TestWorkerLegFingerprintStable(t *testing.T) {
	leg := inject.WorkerLegContext{
		LegID:              "leg-1",
		PhaseID:            "implement",
		CompletionCriteria: []string{"job:complete"},
		LegTools:           []string{"read", "write"},
	}
	a := WorkerLegFingerprint(leg)
	b := WorkerLegFingerprint(leg)
	if a != b {
		t.Fatalf("fingerprints = %q vs %q", a, b)
	}
	leg.PhaseID = "verify"
	c := WorkerLegFingerprint(leg)
	if a == c {
		t.Fatal("phase change should alter worker fingerprint")
	}
}

func TestWorkerLegVolatileFingerprintChangesOnSiblingNotes(t *testing.T) {
	base := inject.WorkerLegContext{LegID: "leg-1", LegTools: []string{"read"}}
	a := WorkerLegVolatileFingerprint(base)
	base.SiblingNotes = []inject.SiblingNote{
		{Agent: "job-a", Summary: "config in resolve.go:40", Ref: "resolve.go:40"},
	}
	b := WorkerLegVolatileFingerprint(base)
	if a == b {
		t.Fatal("sibling notes should alter volatile worker fingerprint")
	}
	base.SiblingNotes = append(base.SiblingNotes, inject.SiblingNote{
		Agent: "job-b", Summary: "use the lexer cache", Ref: "lexer.go:12",
	})
	c := WorkerLegVolatileFingerprint(base)
	if b == c {
		t.Fatal("additional sibling note should alter volatile worker fingerprint")
	}
}

func TestWorkerLegVolatileFingerprintChangesOnReservedPaths(t *testing.T) {
	base := inject.WorkerLegContext{LegID: "leg-1", LegTools: []string{"write"}}
	a := WorkerLegVolatileFingerprint(base)
	base.ReservedPaths = []inject.ReservedPath{{Path: "pkg/foo.go", JobID: "job-a", LegLabel: "implement"}}
	b := WorkerLegVolatileFingerprint(base)
	if a == b {
		t.Fatal("reserved paths should alter volatile worker fingerprint")
	}
}

func TestShouldInjectWorkerLegWhenSiblingNotesArrive(t *testing.T) {
	engine := &AssemblyEngine{}
	leg := inject.WorkerLegContext{LegID: "leg-1", LegTools: []string{"read"}}
	turn := &TurnAssemblyScratch{
		Iteration:            1,
		WorkerLegKey:         WorkerLegFingerprint(leg),
		WorkerLegVolatileKey: WorkerLegVolatileFingerprint(leg),
	}

	if engine.shouldInjectWorkerLeg(turn, leg, "block-without-notes") {
		t.Fatal("expected no inject when stable and volatile peer state are unchanged on iteration 1+")
	}

	leg.SiblingNotes = []inject.SiblingNote{{Agent: "job-a", Summary: "peer note", Ref: "foo.go:1"}}
	if !engine.shouldInjectWorkerLeg(turn, leg, "block-with-notes") {
		t.Fatal("expected re-inject when sibling notes arrive on iteration 1+")
	}

	leg.SiblingNotes = nil
	if engine.shouldInjectWorkerLeg(turn, leg, "block-without-notes-again") {
		t.Fatal("expected no inject after volatile peer feed is consumed")
	}
}

func TestSessionPromptCacheEnabledAlwaysOn(t *testing.T) {
	if settings.DefaultSessionLimits().SettingsFingerprint() == "" {
		t.Fatal("expected non-empty settings fingerprint")
	}
}

func TestActiveWorkflowInjectFingerprint_PhaseListChanges(t *testing.T) {
	snapA := inject.WorkflowRuntimeSnapshot{Phases: []inject.WorkflowPhaseRow{{ID: "research"}}}
	snapB := inject.WorkflowRuntimeSnapshot{Phases: []inject.WorkflowPhaseRow{{ID: "research"}, {ID: "plan"}}}
	a := inject.ActiveWorkflowInjectFingerprint(inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{WorkflowID: "wf"}, Runtime: snapA}), nil)
	b := inject.ActiveWorkflowInjectFingerprint(inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{WorkflowID: "wf"}, Runtime: snapB}), nil)
	if a == b {
		t.Fatal("phase list change should alter inject fingerprint")
	}
}
