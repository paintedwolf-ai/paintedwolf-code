package guard_test

import (
	"github.com/lycaon/lycaon/internal/spawn"
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

// D10: the echo is decided by the Markdown parse, so literal code stays exempt and a
// stray ampersand or tag in the surrounding prose cannot hide the envelope.
func TestTaskEnvelopeEchoFollowsTheParse(t *testing.T) {
	rejectFmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	const envelope = `<task job_id="j1" state="complete"><summary>done</summary></task>`
	for _, tc := range []struct {
		name, content string
		echoes        bool
	}{
		{"the envelope alone", envelope, true},
		{"prose before it", "Here is what came back:\n\n" + envelope, true},
		{"inline in prose", "It returned " + envelope + " so we are done.", true},
		{"an ampersand before it", "Tom & Jerry <b>then</i> " + envelope, true},
		{"inline code", "Never paste `" + envelope + "` into a reply.", false},
		{"a fence", "Never paste:\n\n```xml\n" + envelope + "\n```\n", false},
		{"no job id", "<task state=\"complete\"></task> is the shape.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reject, _ := guard.FormatHostNoToolTurnReject(sess, hostLoopHistory(nil), tc.content, nil,
				spawn.SurfaceImplementSynthesis, true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{})
			if got := strings.Contains(reject, guard.CoordinatorTaskEnvelopeEchoCode); got != tc.echoes {
				t.Fatalf("echo = %v want %v, reject = %q", got, tc.echoes, reject)
			}
		})
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
