package session

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorBatchPhaseToolGuard_packBoardInSynthesize(t *testing.T) {
	sess := &api.Session{ID: "sess-1", Posture: api.SessionPostureBuild, AgentType: "coordinator"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorBatchPhaseTool(
		sess,
		"pack_board",
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		guard.BatchTurnGuard{},
		gc,
	)
	if !guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, guard.CoordinatorBatchWrongPhaseCode) {
		t.Fatalf("want %s Decision for pack_board in synthesize", guard.CoordinatorBatchWrongPhaseCode)
	}
}

func TestQueueCoordinatorGuidanceNudgeUsesAdvisoryDisposition(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	mgr, _ := newTestManager(t)
	mgr.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	mgr.ensureCoordinatorRuntime()
	mgr.Guidance.Queue(t.Context(), "sess-nudge", "COORDINATOR_HOST_TURN_REQUIRES_WAIT", nil, nil)
	if id := mgr.ensureCoordinatorRuntime().Kicks().TakePendingKickID("sess-nudge"); id != "guidance:COORDINATOR_HOST_TURN_REQUIRES_WAIT:session:sess-nudge" {
		t.Fatalf("kick id = %q", id)
	}
	text, _, ok, _ := mgr.ensureCoordinatorRuntime().Kicks().RenderPendingNudge(t.Context(), "sess-nudge", kick.CoordinatorKickRenderContext{})
	if !ok || strings.Contains(text, "Rejected:") || !strings.Contains(text, "Code: COORDINATOR_HOST_TURN_REQUIRES_WAIT") {
		t.Fatalf("nudge = %q ok=%v", text, ok)
	}
}

func TestStripHostBlocks(t *testing.T) {
	in := "Do work\n>>> Spec posture blocked\nRejected: X\nCode: SPEC_POSTURE_STUB_REQUIRED\nCall task(agent_type=foo)\n"
	got := guidance.StripHostBlocks(in)
	if strings.Contains(got, "Spec posture") || strings.Contains(got, "Rejected:") || strings.Contains(got, "Code: SPEC_") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "Do work") {
		t.Fatalf("got %q", got)
	}
	// Host-authored guidance remains intact.
	if !strings.Contains(got, "Call task(agent_type=foo)") {
		t.Fatalf("coordinator-authored line was dropped: %q", got)
	}
}
