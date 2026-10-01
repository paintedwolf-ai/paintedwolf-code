package guard_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func coordinatorRejectFmt(t *testing.T) *guidance.StaticRejectFormatter {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return guidance.NewStaticRejectFormatter(cfg)
}

func TestObserveHostFinishEchoesTaskEnvelope(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	reject, block := guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(

			nil),

		`<task job_id="j1" state="complete"><summary>done</summary></task>`,
		nil,
		"implement_routing",
		true,
		surface.ImplementSessionState{},
		fmt,
		guard.BatchTurnGuard{})

	if !block || !strings.Contains(reject, guard.CoordinatorTaskEnvelopeEchoCode) {
		t.Fatalf("reject=%q block=%v", reject, block)
	}
}

func TestObserveHostTurnRequiresWaitBlocksProseOnly(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	reject, block := guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(

			nil),

		"Implemented the GUI fix — see summary above.",
		nil,
		surface.SurfaceImplementDispatch,
		true,
		surface.ImplementSessionState{},
		fmt,
		guard.BatchTurnGuard{})

	if !block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode) {
		t.Fatalf("reject=%q block=%v", reject, block)
	}
}
