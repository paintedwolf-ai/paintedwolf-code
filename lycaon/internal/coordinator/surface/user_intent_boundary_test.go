package surface

import (
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSelectSurfaceIdleLoopWakeStaysSynthesisAfterInternalScheduledKick(t *testing.T) {
	history := []api.Message{
		visibleTurnMessage("run the auth experiment"),
		{Role: api.MessageRoleAssistant, Content: `<task job_id="j1" agent_type="` + orchestration.ProfileImplementer + `" state="complete"><summary>done</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: "complete"},
		},
		hostNudgeTurnMessage("scheduled wake"),
		hostLoopTurnMessage(),
	}
	profile := ResolveTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		batchReadySessionState(),
	)
	if profile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatal("loop-wake after worker + internal scheduled kick must not select investigate")
	}
	if profile.SurfaceID != "implement_synthesis" {
		t.Fatalf("surface = %q want implement_synthesis", profile.SurfaceID)
	}
}
