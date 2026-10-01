package surface

import (
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// batchReadySessionState is a wrapup-admitted idle snapshot for surface routing fixtures.
func batchReadySessionState() ImplementSessionState {
	return WithWrapupGates(ImplementSessionState{
		WorkersInFlight:   0,
		PendingOverlayIDs: []string{},
	}, true, false)
}

func resolvePickerTurnProfile(
	runCtx api.CoordinatorRunContext,
	sess *api.Session,
	history []api.Message,
	userPrompt string,
	state ...ImplementSessionState,
) TurnProfile {
	switch userPrompt {
	case HostLoopWakeSentinel:
		history = append(history, hostLoopTurnMessage())
	case "Worker task finished — synthesize":
		history = append(history, workerFinishedTurnMessage(userPrompt))
	default:
		history = append(history, visibleTurnMessage(userPrompt))
	}
	return ResolveTurnProfile(enrichPickerRunContext(runCtx), sess, history, state...)
}

func enrichPickerRunContext(runCtx api.CoordinatorRunContext) api.CoordinatorRunContext {
	return EnrichRunContextForWorkflow(runCtx, "")
}

func TestSelectSurfaceInvestigateDefaultAmbient(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"fix the auth bug in src/auth.go",
	)
	if profile.SurfaceID != tools.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, tools.SurfaceImplementInvestigate)
	}
	if profile.ModeRefs[0] != "implement-investigate" {
		t.Fatalf("modeRefs = %v", profile.ModeRefs)
	}
}

func TestSelectSurfaceInvestigateLanguageAgnostic(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"corrige le bug d'authentification dans src/auth.go",
	)
	if profile.SurfaceID != tools.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate for non-English user prompt", profile.SurfaceID)
	}
}

func TestSelectSurfaceInvestigateHardBlockWorkersInFlight(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"fix auth",
		ImplementSessionState{WorkersInFlight: 1},
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("workers in flight must not select investigate")
	}
	if profile.SurfaceID != SurfaceImplementPark {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, SurfaceImplementPark)
	}
}

func TestSelectSurfaceInvestigateHardBlockPlanCatalog(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "plan", CurrentPhase: "research", SurfaceProfile: "plan"},
		&api.Session{Posture: api.SessionPostureSpec},
		nil,
		"research the repo",
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("plan catalog must not select investigate")
	}
	if profile.SurfaceID != "plan_research" {
		t.Fatalf("surface = %q want plan_research", profile.SurfaceID)
	}
}

func TestSelectSurfaceInvestigateHardBlockComposeDraft(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{HasComposeDraft: true},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"compose workflow",
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("compose draft must not select investigate")
	}
}

func TestSelectSurfaceInvestigateHardBlockLoopWakeAfterWorkerCompletion(t *testing.T) {
	history := completeImplementerHistoryForPicker()
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		HostLoopWakeSentinel,
	)
	if profile.SurfaceID != tools.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate on loop wake without batch readiness", profile.SurfaceID)
	}
}

func TestSelectSurfaceHostCycleOpenPlanUsesDispatch(t *testing.T) {
	history := completeImplementerHistoryForPicker()
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{
			WorkflowID:                   "bugbash",
			WorkflowDefaultExecutionMode: ExecutionModeFamilyOrchestrate,
		},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		"Worker task finished — synthesize",
		ImplementSessionState{ProgressOpenCount: 1},
	)
	if profile.SurfaceID != SurfaceImplementDispatch {
		t.Fatalf("host cycle with open plan surface = %q want %q", profile.SurfaceID, SurfaceImplementDispatch)
	}
}

func TestSelectSurfaceHostCycleOpenPlanAfterReadScoutUsesInvestigate(t *testing.T) {
	history := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="repo-researcher" state="complete"><summary>surveyed layout</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "j1",
			Status:   "complete",
		},
	}}
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		"Worker task finished — synthesize",
		ImplementSessionState{ProgressOpenCount: 1},
	)
	if profile.SurfaceID != tools.SurfaceImplementInvestigate {
		t.Fatalf("read scout with open plan surface = %q want investigate", profile.SurfaceID)
	}
}

func TestSelectSurfaceIdleLoopWakeContinuesInvestigateAfterVisibleUser(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "Let's improve the physics in pacifism.py"},
		{Role: api.MessageRoleAssistant, Content: "Reading pacifism.py now."},
	}
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		HostLoopWakeSentinel,
	)
	if profile.SurfaceID != tools.SurfaceImplementInvestigate {
		t.Fatalf("idle loop wake after visible user surface = %q want investigate", profile.SurfaceID)
	}
}

func TestSelectSurfaceIdleLoopWakeStaysOrchestrateAfterWorkerSinceVisibleUser(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "dispatch fixes"},
		{Role: api.MessageRoleAssistant, Content: `<task job_id="j1" agent_type="` + orchestration.ProfileImplementer + `" state="complete"><summary>done</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: "complete"},
		},
	}
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		HostLoopWakeSentinel,
	)
	if profile.SurfaceID != tools.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate without batch readiness", profile.SurfaceID)
	}
}

func TestSelectSurfaceInvestigateHardBlockPendingOverlay(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		pendingMergeHistoryForPicker(),
		"fix auth",
		ImplementSessionState{PendingOverlayIDs: []string{"j1"}},
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("pending overlay promote must not select investigate")
	}
}

func TestSelectSurfaceInvestigateHardBlockChildSubroutine(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{
			WorkflowID: "implement",
			RunStatus:  string(api.WorkflowRunStatusPausedOnChild),
		},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"fix auth",
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("child subroutine must not select investigate")
	}
	if profile.SurfaceID != SurfaceImplementDispatch {
		t.Fatalf("surface = %q want %q on visible user orchestrate path", profile.SurfaceID, SurfaceImplementDispatch)
	}
}

func TestSelectSurfaceInvestigateYieldsToWorkersInFlight(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{
			WorkflowID:                   "custom-investigate",
			WorkflowDefaultExecutionMode: ExecutionModeFamilyInvestigate,
		},
		&api.Session{Posture: api.SessionPostureBuild},
		completeImplementerHistoryForPicker(),
		HostLoopWakeSentinel,
		ImplementSessionState{WorkersInFlight: 2},
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("investigate default yields to workers-in-flight hard block")
	}
	if profile.SurfaceID != SurfaceImplementPark {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, SurfaceImplementPark)
	}
}

func TestSelectSurfaceTransitionAfterTaskDispatch(t *testing.T) {
	history := completeImplementerHistoryForPicker()
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		HostLoopWakeSentinel,
		ImplementSessionState{WorkersInFlight: 2},
	)
	if profile.SurfaceID != SurfaceImplementPark {
		t.Fatalf("after task dispatch surface = %q want %q", profile.SurfaceID, SurfaceImplementPark)
	}
}

func TestSelectSurfaceWorkerFinishedWithSiblingsUsesDispatch(t *testing.T) {
	history := completeImplementerHistoryForPicker()
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		"Worker task finished — synthesize",
		ImplementSessionState{WorkersInFlight: 2},
	)
	if profile.SurfaceID != SurfaceImplementDispatch {
		t.Fatalf("worker finished with siblings surface = %q want %q", profile.SurfaceID, SurfaceImplementDispatch)
	}
	if profile.ModeRefs[0] != "implement-dispatch" {
		t.Fatalf("modeRefs = %v want implement-dispatch", profile.ModeRefs)
	}
}

func TestSelectSurfaceTransitionBackToInvestigateAfterWorkersIdle(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		completeImplementerHistoryForPicker(),
		"summarize what changed",
	)
	if profile.SurfaceID != tools.SurfaceImplementInvestigate {
		t.Fatalf("idle visible user turn surface = %q want investigate", profile.SurfaceID)
	}
}

func TestSelectSurfaceWorkflowDefaultOrchestrateSuppressesInvestigate(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{
			WorkflowID:                   "bugbash",
			WorkflowDefaultExecutionMode: ExecutionModeFamilyOrchestrate,
		},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"fix auth",
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatalf("Tier 1 orchestrate default must not select investigate, got %q", profile.SurfaceID)
	}
	if profile.SurfaceID != SurfaceImplementDispatch {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, SurfaceImplementDispatch)
	}
}

func TestSelectSurfacePhaseStampOrchestrateBeatsWorkflowInvestigateDefault(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{
			WorkflowID:                   "custom-investigate",
			PhaseExecutionMode:           ExecutionModeFamilyOrchestrate,
			WorkflowDefaultExecutionMode: ExecutionModeFamilyInvestigate,
		},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"dispatch fixes",
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("Tier 2 orchestrate stamp must beat Tier 1 investigate default")
	}
	if profile.SurfaceID != SurfaceImplementDispatch {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, SurfaceImplementDispatch)
	}
}

func TestSelectSurfaceWorkflowInvestigateDefaultWithWorkersInFlight(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{
			WorkflowID:                   "custom-investigate",
			WorkflowDefaultExecutionMode: ExecutionModeFamilyInvestigate,
		},
		&api.Session{Posture: api.SessionPostureBuild},
		completeImplementerHistoryForPicker(),
		HostLoopWakeSentinel,
		ImplementSessionState{WorkersInFlight: 1},
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("P1 workers in flight must block investigate even with Tier 1 investigate default")
	}
}

func completeImplementerHistoryForPicker() []api.Message {
	return []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="` + orchestration.ProfileImplementer + `" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "j1",
			Status:   "complete",
		},
	}}
}

func pendingMergeHistoryForPicker() []api.Message {
	return []api.Message{{
		Role: api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="implementer" state="complete" merge_status="pending">
  <summary>wrote file</summary>
</task>`,
		WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: "complete"},
	}}
}
