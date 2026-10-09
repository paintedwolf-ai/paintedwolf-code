package policyfeedback

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBlockFeedbackRetainsIdentityWithoutRenderer(t *testing.T) {
	feedbackService := New()
	res := &oar.PipelineResult{Enforced: true, Decision: &oar.Decision{
		Effect: oar.EffectBlock, Code: "DENIED", Rule: "example/DENIED",
		Data: map[string]any{"subject": map[string]any{"kind": "task", "id": "target"}},
	}}
	refusal, blocked, err := feedbackService.RenderResult(t.Context(), oar.AnchorToolPreInvoke, res)
	testutil.FailErr(t, "render fallback block", err)
	if !blocked || refusal == nil || len(refusal.Facts.Feedback) != 1 {
		t.Fatalf("fallback block lost feedback: %#v", refusal)
	}
	feedback := refusal.Facts.Feedback[0]
	if feedback.Details["policy_rule"] != "example/DENIED" || feedback.Subject == nil || feedback.Subject.ID != "target" {
		t.Fatalf("fallback lost rule or subject: %#v", feedback)
	}
}
