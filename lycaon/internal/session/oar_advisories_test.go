package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func advisoryTestManager(t *testing.T) *Manager {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	mgr := &Manager{rejectFmt: rejectFmt}
	mgr.ensureCoordinatorRuntime()
	mgr.SetOARPipeline(nil, oar.NewRenderer(rejectFmt, nil))
	return mgr
}

func TestQueueCoordinatorGuidanceAdvisoriesStacksDistinctCodes(t *testing.T) {
	mgr := advisoryTestManager(t)
	ctx := t.Context()
	mgr.queueCoordinatorGuidanceAdvisories(ctx, "sess-adv", []GuidanceNudge{
		{Code: "COORDINATOR_UNGROUNDED_CLAIM"},
		{Code: "COORDINATOR_HOST_TURN_REQUIRES_WAIT"},
	})
	first := mgr.ensureCoordinatorRuntime().Kicks().TakePendingKickID("sess-adv")
	if first != "guidance:COORDINATOR_UNGROUNDED_CLAIM:session:sess-adv" {
		t.Fatalf("first kick = %q", first)
	}
	text, lease, ok, _ := mgr.ensureCoordinatorRuntime().Kicks().RenderPendingNudge(ctx, "sess-adv", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "Code: COORDINATOR_UNGROUNDED_CLAIM") {
		t.Fatalf("first nudge = %q ok=%v", text, ok)
	}
	mgr.ensureCoordinatorRuntime().Kicks().AckPendingNudge("sess-adv", lease)
	second := mgr.ensureCoordinatorRuntime().Kicks().TakePendingKickID("sess-adv")
	if second != "guidance:COORDINATOR_HOST_TURN_REQUIRES_WAIT:session:sess-adv" {
		t.Fatalf("second kick = %q", second)
	}
	text, _, ok, _ = mgr.ensureCoordinatorRuntime().Kicks().RenderPendingNudge(ctx, "sess-adv", kick.CoordinatorKickRenderContext{})
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
	refuse, blocked, err := mgr.renderOARResult(context.Background(), oar.AnchorToolPost, res)
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

func TestBlockFeedbackRetainsIdentityWithoutRenderer(t *testing.T) {
	mgr := &Manager{}
	res := &oar.PipelineResult{Enforced: true, Decision: &oar.Decision{
		Effect: oar.EffectBlock, Code: "DENIED", Rule: "example/DENIED",
		Data: map[string]any{"subject": map[string]any{"kind": "task", "id": "target"}},
	}}
	refusal, blocked, err := mgr.renderOARResult(t.Context(), oar.AnchorToolPreInvoke, res)
	testutil.FailErr(t, "render fallback block", err)
	if !blocked || refusal == nil || len(refusal.Facts.Feedback) != 1 {
		t.Fatalf("fallback block lost feedback: %#v", refusal)
	}
	feedback := refusal.Facts.Feedback[0]
	if feedback.Details["policy_rule"] != "example/DENIED" || feedback.Subject == nil || feedback.Subject.ID != "target" {
		t.Fatalf("fallback lost rule or subject: %#v", feedback)
	}
}

func TestErrGroundingNudgeSingleCodeQueues(t *testing.T) {
	mgr := advisoryTestManager(t)
	nudge := &ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM"}
	mgr.queueCoordinatorGuidanceAdvisories(t.Context(), "sess-one", nudge.Nudges())
	if id := mgr.ensureCoordinatorRuntime().Kicks().TakePendingKickID("sess-one"); id != "guidance:COORDINATOR_UNGROUNDED_CLAIM:session:sess-one" {
		t.Fatalf("kick id = %q", id)
	}
}

func TestCopy1QueuesFrozenExtensionAdvisories(t *testing.T) {
	mgr := advisoryTestManager(t)
	decision := &oar.Decision{Effect: oar.EffectNudge, Code: "example/EXTENSION", Advisories: []oar.Advisory{
		{Code: "example/EXTENSION", Copy: map[string]string{"what": "Frozen extension copy"}},
	}}
	testutil.FailErr(t, "queue frozen advisory", mgr.queueOARAdvisories(t.Context(), "session", oar.AnchorCoordinatorPostTurn, decision))
	lease := mgr.ensureCoordinatorRuntime().Kicks().LeasePolicyFeedback("session")
	if len(lease.Entries) != 1 {
		t.Fatalf("queued entries = %d", len(lease.Entries))
	}
	message, err := mgr.policyFeedbackMessage(t.Context(), lease)
	testutil.FailErr(t, "render queued feedback", err)
	text := message.Content
	if !strings.Contains(text, "Frozen extension copy") || !strings.Contains(text, "example/EXTENSION") {
		t.Fatalf("[OAR-COPY-1] extension advisory lost its rendered copy or identity: %q", text)
	}
}

func TestEval20QueuesAllNamespacedAdvisoriesInOneDelivery(t *testing.T) {
	mgr := advisoryTestManager(t)
	decision := &oar.Decision{Effect: oar.EffectWarn, Code: "SHARED"}
	for i := range 40 {
		decision.Advisories = append(decision.Advisories, oar.Advisory{
			Code: "SHARED", Rule: fmt.Sprintf("namespace.r%d/SHARED", i),
			Copy: map[string]string{"what": fmt.Sprintf("Frozen advisory %02d", i)},
		})
	}
	testutil.FailErr(t, "queue namespaced advisories", mgr.queueOARAdvisories(t.Context(), "session", oar.AnchorContentOutput, decision))
	lease := mgr.ensureCoordinatorRuntime().Kicks().LeasePolicyFeedback("session")
	message, err := mgr.policyFeedbackMessage(t.Context(), lease)
	testutil.FailErr(t, "render queued feedback", err)
	text := message.Content
	ok := len(lease.Entries) == 40
	if !ok || strings.Count(text, "Code: SHARED") != 40 {
		t.Fatalf("[OAR-EVAL-20] advisories collided or exceeded the queue: %q, ok=%v", text, ok)
	}
	previous := -1
	for i := range 40 {
		index := strings.Index(text, fmt.Sprintf("Frozen advisory %02d", i))
		if index <= previous {
			t.Fatalf("[OAR-EVAL-20] advisory %d missing or out of order: %q", i, text)
		}
		previous = index
	}
}

// [OAR-COPY-1] A queued grounding warning must keep the evaluated branch.
func TestGroundingNudgeQueuesFrozenCopy(t *testing.T) {
	mgr := advisoryTestManager(t)
	nudge := &ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM", Data: map[string]any{"count": 99}, Copy: map[string]string{"what": "Measured 3 claims", "fix": "Keep literal {{ count }}"}}
	mgr.queueCoordinatorGuidanceAdvisories(t.Context(), "frozen", nudge.Nudges())
	if mgr.ensureCoordinatorRuntime().Kicks().TakePendingKickID("frozen") == "" {
		t.Fatal("frozen nudge was not queued")
	}
	text, _, ok, err := mgr.ensureCoordinatorRuntime().Kicks().RenderPendingNudge(t.Context(), "frozen", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render frozen grounding warning", err)
	if !ok || !strings.Contains(text, "Measured 3 claims") || !strings.Contains(text, "{{ count }}") || strings.Contains(text, "Rejected:") {
		t.Fatalf("lost frozen advisory: %s", text)
	}
}

// [OAR-COPY-1] A guard conversion must keep both the evaluated display and facts.
func TestGuardRefusalPreservesFrozenCopy(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	nudge := &ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM", Data: map[string]any{"count": 99}, Copy: map[string]string{"what": "Measured 3 claims", "fix": "Retain {{ count }} literally"}}
	reject := rejectFromGuardErr(t.Context(), nudge)
	if reject == nil || !strings.Contains(reject.Body, "Measured 3 claims") || !strings.Contains(reject.Body, "{{ count }}") || !errors.Is(reject, nudge) {
		t.Fatalf("guard conversion lost frozen delivery or cause: %#v", reject)
	}
}
