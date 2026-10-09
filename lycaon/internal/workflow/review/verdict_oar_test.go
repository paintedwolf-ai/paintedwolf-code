package review

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowVerdictRefusalsRetainPolicyOccurrenceAndStructuredCause(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load workflow hints", err)
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	plane := tools.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(guidance.NewStaticRejectFormatter(hints), nil)}
	for _, code := range []string{
		workflowvalidation.ReviewLoopVerdictInvalidCode, SubmitVerdictUnavailableCode, SubmitVerdictIterationCapCode,
		SubmitVerdictReviewerMissingCode, guidance.VerdictCitationsRequiredCode,
		guidance.VerdictCitationUngroundedCode, guidance.VerdictReviewerUncitedCode,
	} {
		t.Run(code, func(t *testing.T) {
			rule, ok := rules.Get(code)
			if !ok {
				t.Fatalf("missing rule %s", code)
			}
			rule.OnFire = []oar.OnFireAction{oar.OnFireIncrementCounter}
			ctx := curationctx.WithSession(t.Context(), curationctx.Session{SessionID: t.Name()})
			out := &tools.ToolInvocationOut{}
			details := map[string]any{
				"reason": "invalid {{ literal }} field", "expected_call": "submit_verdict(current schema)",
				"attempt": 4, "iteration_cap": 4, "terminal_value": "DECIDED",
				"missing_reviewers": []string{"reviewer-1"}, "uncited_reviewers": []string{"reviewer-2"},
				"ungrounded_sample": []string{"missing#3"}, "observed_handles": []string{"observed#4"},
			}
			body, original := rejectSubmitVerdict(tools.ToolContext{Out: out}, code, "review-phase", details)
			reject := tools.AsToolReject(original)
			if body != "" || reject == nil || reject.Code != code {
				t.Fatalf("verdict refusal: output=%q error=%v want %s", body, original, code)
			}
			failure := plane.RejectObservation(ctx, "submit_verdict", "coordinator", nil, reject)
			refusal, ok := guidance.RefusalFromError(failure)
			if !ok || refusal.Code() != code || tools.AsToolReject(failure) != reject || len(refusal.Copy) != 5 {
				t.Fatalf("lost evaluated refusal or original cause: %v", failure)
			}
			if count := pipeline.Counters().Get(t.Name(), rule.Qualified(), oar.CounterFire); count != 1 {
				t.Fatalf("refusal fired %d times", count)
			}
			if code == workflowvalidation.ReviewLoopVerdictInvalidCode && !strings.Contains(refusal.Copy["cause"], "invalid {{ literal }} field") {
				t.Fatalf("host observation was lost or reinterpreted: %+v", refusal.Copy)
			}
			feedback := out.Facts.FeedbackFor(code)
			if out.Facts.Resolution() != api.ToolResultOutcomeRejected || feedback.Subject == nil || feedback.Subject.ID != "review-phase" {
				t.Fatalf("lost phase feedback identity: %+v", out.Facts)
			}
			if feedback.Details["expected_call"] != details["expected_call"] {
				t.Fatalf("lost current schema: %+v", feedback.Details)
			}
		})
	}
}
