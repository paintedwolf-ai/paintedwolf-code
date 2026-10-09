package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func advisoryTestManager(t *testing.T) *Host {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	mgr, _ := newTestManager(t)
	mgr.SetRejectFormatter(rejectFmt)
	mgr.Coordinator.Runtime
	mgr.SetOARPipeline(nil, oar.NewRenderer(rejectFmt, nil))
	return mgr
}

func TestQueueCoordinatorGuidanceAdvisoriesStacksDistinctCodes(t *testing.T) {
	mgr := advisoryTestManager(t)
	ctx := t.Context()
	mgr.Coordinator.Guidance.QueueAdvisories(ctx, "sess-adv", []guidance.GuidanceNudge{
		{Code: "COORDINATOR_UNGROUNDED_CLAIM"},
		{Code: "COORDINATOR_HOST_TURN_REQUIRES_WAIT"},
	})
	first := mgr.Coordinator.Runtime.Kicks().TakePendingKickID("sess-adv")
	if first != "guidance:COORDINATOR_UNGROUNDED_CLAIM:session:sess-adv" {
		t.Fatalf("first kick = %q", first)
	}
	text, lease, ok, _ := mgr.Coordinator.Runtime.Kicks().RenderPendingNudge(ctx, "sess-adv", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "Code: COORDINATOR_UNGROUNDED_CLAIM") {
		t.Fatalf("first nudge = %q ok=%v", text, ok)
	}
	mgr.Coordinator.Runtime.Kicks().AckPendingNudge("sess-adv", lease)
	second := mgr.Coordinator.Runtime.Kicks().TakePendingKickID("sess-adv")
	if second != "guidance:COORDINATOR_HOST_TURN_REQUIRES_WAIT:session:sess-adv" {
		t.Fatalf("second kick = %q", second)
	}
	text, _, ok, _ = mgr.Coordinator.Runtime.Kicks().RenderPendingNudge(ctx, "sess-adv", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "Code: COORDINATOR_HOST_TURN_REQUIRES_WAIT") {
		t.Fatalf("second nudge = %q ok=%v", text, ok)
	}
}

func TestRenderOARResultConcatenatesWarnAdvisories(t *testing.T) {
	mgr := advisoryTestManager(t)
	res := &oar.PipelineResult{
		Enforced: true,
		Decision: &oar.Decision{
			Effect: oar.EffectWarn,
			Code:   "DOOM_LOOP_REPEAT_WARN",
			Advisories: []oar.Advisory{
				{Code: "DOOM_LOOP_REPEAT_WARN"},
				{Code: "DOOM_LOOP_FRUITLESS_SEARCH"},
			},
		},
	}
	refuse, blocked, err := mgr.Coordinator.Feedback.RenderResult(context.Background(), oar.AnchorToolPost, res)
	testutil.FailErr(t, "renderOARResult", err)
	if blocked || refuse == nil {
		t.Fatalf("blocked=%v refuse=%v", blocked, refuse)
	}
	if refuse.Code() != "DOOM_LOOP_REPEAT_WARN" {
		t.Fatalf("envelope code = %q, want first advisory", refuse.Code())
	}
	if !strings.Contains(refuse.Body, "DOOM_LOOP_REPEAT_WARN") || !strings.Contains(refuse.Body, "DOOM_LOOP_FRUITLESS_SEARCH") {
		t.Fatalf("body dropped a warn advisory: %q", refuse.Body)
	}
}

func TestErrGroundingNudgeSingleCodeQueues(t *testing.T) {
	mgr := advisoryTestManager(t)
	nudge := &guidance.ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM"}
	mgr.Coordinator.Guidance.QueueAdvisories(t.Context(), "sess-one", nudge.Nudges())
	if id := mgr.Coordinator.Runtime.Kicks().TakePendingKickID("sess-one"); id != "guidance:COORDINATOR_UNGROUNDED_CLAIM:session:sess-one" {
		t.Fatalf("kick id = %q", id)
	}
}

// [OAR-COPY-1] A queued grounding warning must keep the evaluated branch.
func TestGroundingNudgeQueuesFrozenCopy(t *testing.T) {
	mgr := advisoryTestManager(t)
	nudge := &guidance.ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM", Data: map[string]any{"count": 99}, Copy: map[string]string{"what": "Measured 3 claims", "fix": "Keep literal {{ count }}"}}
	mgr.Coordinator.Guidance.QueueAdvisories(t.Context(), "frozen", nudge.Nudges())
	if mgr.Coordinator.Runtime.Kicks().TakePendingKickID("frozen") == "" {
		t.Fatal("frozen nudge was not queued")
	}
	text, _, ok, err := mgr.Coordinator.Runtime.Kicks().RenderPendingNudge(t.Context(), "frozen", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render frozen grounding warning", err)
	if !ok || !strings.Contains(text, "Measured 3 claims") || !strings.Contains(text, "{{ count }}") || strings.Contains(text, "Rejected:") {
		t.Fatalf("lost frozen advisory: %s", text)
	}
}

// [OAR-COPY-1] A guard conversion must keep both the evaluated display and facts.
func TestGuardRefusalPreservesFrozenCopy(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	nudge := &guidance.ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM", Data: map[string]any{"count": 99}, Copy: map[string]string{"what": "Measured 3 claims", "fix": "Retain {{ count }} literally"}}
	reject := policyfacts.RejectFromGuardErr(t.Context(), nudge)
	if reject == nil || !strings.Contains(reject.Body, "Measured 3 claims") || !strings.Contains(reject.Body, "{{ count }}") || !errors.Is(reject, nudge) {
		t.Fatalf("guard conversion lost frozen delivery or cause: %#v", reject)
	}
}
