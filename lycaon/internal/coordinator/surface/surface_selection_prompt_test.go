package surface

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestSurfaceSelectionUserPrompt_skipsInTurnNudges(t *testing.T) {
	history := []api.Message{
		visibleTurnMessage("Review docs and compare online"),
		{Role: api.MessageRoleAssistant, Content: "surveying"},
		hostNudgeTurnMessage("citation grounding retry"),
	}
	if got := SurfaceSelectionUserPrompt(history); got != "Review docs and compare online" {
		t.Fatalf("SurfaceSelectionUserPrompt = %q", got)
	}
}

func TestSurfaceSelectionUserPrompt_visibleUserTurn(t *testing.T) {
	history := []api.Message{
		visibleTurnMessage("Review docs and compare online"),
		{Role: api.MessageRoleAssistant, Content: "dispatching scout"},
	}
	if got := SurfaceSelectionUserPrompt(history); got != "Review docs and compare online" {
		t.Fatalf("SurfaceSelectionUserPrompt = %q", got)
	}
}

func TestIsVisibleUserTurn_rejectsCoordinatorHostRetryNudge(t *testing.T) {
	if isVisibleUserTurn([]api.Message{hostNudgeTurnMessage("citation grounding retry")}) {
		t.Fatal("coordinator host retry nudge must not count as visible user turn")
	}
}

func TestResolveTurnProfile_citationNudgeDoesNotRerouteFromSynthesis(t *testing.T) {
	base := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="implementer" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "j1",
			Status:   "complete",
		},
	}}
	history := append(append([]api.Message(nil), base...),
		workerFinishedTurnMessage("worker task finished"),
		api.Message{Role: api.MessageRoleAssistant, Content: "closeout attempt"},
		guidanceTurnMessage("citation grounding retry", "SYNTH_HANDLE_NOT_IN_LEGS"),
	)
	state := WithWrapupGates(ImplementSessionState{}, true, false)
	runCtx := api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"}
	profile := ResolveTurnProfile(runCtx, &api.Session{Posture: api.SessionPostureBuild}, history, state)
	if profile.SurfaceID != "implement_synthesis" {
		t.Fatalf("anchor surface = %q want implement_synthesis", profile.SurfaceID)
	}
	if profile.ModeRefs[0] != "implement-synthesis" {
		t.Fatalf("modeRefs = %v", profile.ModeRefs)
	}
	emptyRun := ResolveTurnProfile(api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild}, history, state)
	if emptyRun.SurfaceID == "implement_synthesis" {
		t.Fatalf("empty workflow must not select synthesis; got %q", emptyRun.SurfaceID)
	}
}
