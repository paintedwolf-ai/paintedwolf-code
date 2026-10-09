package surface

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestComputeSurfaceFacts_reflectsSources(t *testing.T) {
	history := []api.Message{
		visibleTurnMessage("Survey the repo"),
		{Role: api.MessageRoleAssistant, Content: `<task job_id="j1" status="complete">done</task>`},
		hostLoopTurnMessage(),
	}
	runCtx := api.CoordinatorRunContext{
		HasComposeDraft:              true,
		WorkflowID:                   "plan",
		CurrentPhase:                 "research",
		PhaseCoordinatorSurface:      "plan_research",
		RunStatus:                    string(api.WorkflowRunStatusPausedOnChild),
		WorkflowDefaultExecutionMode: ExecutionModeFamilyOrchestrate,
	}
	eligible := false
	runCtx.WorkflowInvestigateEligible = &eligible
	sess := &api.Session{
		Posture: api.SessionPostureSpec,
	}
	state := WithWrapupGates(ImplementSessionState{
		WorkersInFlight:   2,
		PendingOverlayIDs: []string{"j-overlay"},
	}, true, true)

	facts := ComputeSurfaceFacts(runCtx, sess, history, state)

	if !facts.HasComposeDraft {
		t.Fatal("HasComposeDraft")
	}
	if facts.ManifestBoundSurface != "plan_research" {
		t.Fatalf("ManifestBoundSurface = %q", facts.ManifestBoundSurface)
	}
	if facts.OverlayPromotePending != 1 {
		t.Fatalf("OverlayPromotePending = %d want 1", facts.OverlayPromotePending)
	}
	if !facts.ChildSubroutineBlocksInvestigate {
		t.Fatal("ChildSubroutineBlocksInvestigate")
	}
	if facts.WorkflowDeclaredMode != ExecutionModeFamilyOrchestrate {
		t.Fatalf("WorkflowDeclaredMode = %q", facts.WorkflowDeclaredMode)
	}
	if facts.InvestigateDefaultEligible {
		t.Fatal("InvestigateDefaultEligible should be false")
	}
	if !facts.InvestigateHardBlock {
		t.Fatal("InvestigateHardBlock expected for paused_on_child + host cycle")
	}
	if facts.WorkersInFlight != 2 {
		t.Fatalf("WorkersInFlight = %d", facts.WorkersInFlight)
	}
	if !facts.HostCycleTurn {
		t.Fatal("HostCycleTurn")
	}
	if !facts.OpenRepairSinceUserIntent {
		t.Fatal("OpenRepairSinceUserIntent")
	}
	if !facts.BatchReadyForSynthesis {
		t.Fatal("BatchReadyForSynthesis")
	}
	if facts.Posture != "spec" {
		t.Fatalf("Posture = %q", facts.Posture)
	}
}

func TestComputeSurfaceFacts_visibleUserClearsHardBlockWhenStructuralGuardsClear(t *testing.T) {
	runCtx := enrichPickerRunContext(api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"})
	sess := &api.Session{Posture: api.SessionPostureBuild}
	facts := ComputeSurfaceFacts(runCtx, sess, []api.Message{visibleTurnMessage("fix the auth bug")}, ImplementSessionState{})
	if facts.InvestigateHardBlock {
		t.Fatal("visible user turn should clear soft investigate blocks")
	}
	if facts.HostCycleTurn {
		t.Fatal("visible user is not a host-cycle turn")
	}
	if !facts.InvestigateDefaultEligible {
		t.Fatal("enriched ambient implement should be investigate-default eligible")
	}
}

func TestComputeSurfaceFacts_investigateDefaultEligibleAmbient(t *testing.T) {
	runCtx := enrichPickerRunContext(api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"})
	facts := ComputeSurfaceFacts(runCtx, &api.Session{}, []api.Message{visibleTurnMessage("hello")}, ImplementSessionState{})
	if !facts.InvestigateDefaultEligible {
		t.Fatal("enriched ambient implement run should be investigate-default eligible")
	}
}

func TestSurfaceFactsAsMap_matchesRegistryKeys(t *testing.T) {
	m := SurfaceFacts{
		HasComposeDraft:                  true,
		ManifestBoundSurface:             toolcontract.SurfaceImplementInvestigate,
		OverlayPromotePending:            3,
		ChildSubroutineBlocksInvestigate: false,
		WorkflowDeclaredMode:             ExecutionModeFamilyInvestigate,
		InvestigateDefaultEligible:       true,
		InvestigateHardBlock:             false,
		WorkersInFlight:                  1,
		HostCycleTurn:                    true,
		OpenRepairSinceUserIntent:        false,
		BatchReadyForSynthesis:           true,
		Posture:                          "build",
	}.AsMap()
	for _, name := range RegisteredSurfaceFactNames() {
		if _, ok := m[name]; !ok {
			t.Fatalf("AsMap missing registered fact %q", name)
		}
	}
	if len(m) != len(registeredSurfaceFactNames) {
		t.Fatalf("AsMap len = %d want %d", len(m), len(registeredSurfaceFactNames))
	}
}

func TestSurfaceFactsRegistry_structFieldClosure(t *testing.T) {
	fieldToFact := map[string]string{
		"HasComposeDraft":                       FactHasComposeDraft,
		"ManifestBoundSurface":                  FactManifestBoundSurface,
		"OverlayPromotePending":                 FactOverlayPromotePending,
		"ChildSubroutineBlocksInvestigate":      FactChildSubroutineBlocksInvestigate,
		"WorkflowDeclaredMode":                  FactWorkflowDeclaredMode,
		"InvestigateDefaultEligible":            FactInvestigateDefaultEligible,
		"InvestigateHardBlock":                  FactInvestigateHardBlock,
		"WorkersInFlight":                       FactWorkersInFlight,
		"HostCycleTurn":                         FactHostCycleTurn,
		"WorkerTaskFinishedTurn":                FactWorkerTaskFinishedTurn,
		"HostLoopWakeTurn":                      FactHostLoopWakeTurn,
		"OpenRepairSinceUserIntent":             FactOpenRepairSinceUserIntent,
		"BatchReadyForSynthesis":                FactBatchReadyForSynthesis,
		"WrapupGatesLoaded":                     FactWrapupGatesLoaded,
		"VerifyUnverified":                      FactVerifyUnverified,
		"VisibleUserTurn":                       FactVisibleUserTurn,
		"Posture":                               FactPosture,
		"ProgressHasOpenSteps":                  FactProgressHasOpenSteps,
		"ProgressMissing":                       FactProgressMissing,
		"ProgressGatedToolAttemptedSinceIntent": FactProgressGatedToolAttemptedSinceIntent,
		"PhaseHostHeld":                         FactPhaseHostHeld,
	}
	if len(fieldToFact) != len(registeredSurfaceFactNames) {
		t.Fatalf("field map len = %d registered = %d", len(fieldToFact), len(registeredSurfaceFactNames))
	}
	seen := make(map[string]struct{}, len(fieldToFact))
	for field, fact := range fieldToFact {
		if !IsKnownFact(fact) {
			t.Fatalf("field %s maps to unknown fact %q", field, fact)
		}
		seen[fact] = struct{}{}
	}
	for _, name := range RegisteredSurfaceFactNames() {
		if _, ok := seen[name]; !ok {
			t.Fatalf("registered fact %q has no SurfaceFacts field", name)
		}
	}
}

func TestIsKnownFact_closure(t *testing.T) {
	for _, name := range RegisteredSurfaceFactNames() {
		if !IsKnownFact(name) {
			t.Fatalf("IsKnownFact(%q) = false", name)
		}
	}
	for _, unknown := range []string{"", "not_a_fact", "batch_phase"} {
		if IsKnownFact(unknown) {
			t.Fatalf("IsKnownFact(%q) should be false", unknown)
		}
	}
	if len(factKinds) != len(registeredSurfaceFactNames) {
		t.Fatalf("factKinds len = %d registered = %d", len(factKinds), len(registeredSurfaceFactNames))
	}
}
