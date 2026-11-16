package surface

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

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
