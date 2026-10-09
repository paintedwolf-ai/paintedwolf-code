package promptloop

import (
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutputDeliveryRefusalRetainsExecutionAndEvaluatesOnce(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools: ToolsDeps{
			BlockPlane: &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(nil, nil)},
		},
	})
	for _, code := range []string{tooloutput.ToolResultTooLargeCode, tooloutput.ToolOutputSpillCapExceededCode, tooloutput.ToolOutputSpillUnavailableCode} {
		rule, _ := rules.Get(code)
		rule.OnFire = []oar.OnFireAction{oar.OnFireIncrementCounter}
		for _, outcome := range []api.ToolResultOutcome{api.ToolResultOutcomeCompleted, api.ToolResultOutcomeError} {
			t.Run(code+"/"+string(outcome), func(t *testing.T) {
				sess := &api.Session{ID: t.Name()}
				run := toolInvocation{invoked: true, facts: (guidance.ToolResultFacts{Outcome: outcome}).WithCode("PRIOR_ADVISORY"), captures: toolCaptures{ownerRef: "existing-operation"}}
				if outcome == api.ToolResultOutcomeError {
					run.failure = &api.InvocationFailure{Code: "ORIGINAL_FAILURE", Class: api.FailureClassOwnerError}
				}
				data := map[string]any{"bytes": 2048, "cap": 1024}
				got := loop.Tools.refuseOutputDelivery(t.Context(), sess, api.ToolCall{Name: "command"}, tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "coordinator"}}, run, code, data)
				details := got.facts.FeedbackFor(code).Details
				if got.facts.PrimaryCode() != code || !got.facts.HasCode("PRIOR_ADVISORY") {
					t.Fatalf("settled refusal identity or prior advisory lost: %+v", got.facts)
				}
				if !got.invoked || got.captures.ownerRef != run.captures.ownerRef || details["execution_outcome"] != string(outcome) {
					t.Fatalf("execution state lost: %+v", got)
				}
				if got.reject == nil || len(got.reject.Copy) != 5 || !strings.Contains(got.reject.Copy["cause"], string(outcome)) {
					t.Fatalf("evaluated copy lost execution outcome: %+v", got.reject)
				}
				if got.failure.CallerFault() || got.failure.Retryable {
					t.Fatalf("output failure became a caller retry: %+v", got.failure)
				}
				if outcome == api.ToolResultOutcomeError && details["execution_failure_code"] != "ORIGINAL_FAILURE" {
					t.Fatalf("original failure lost: %+v", details)
				}
				if _, changed := data["execution_outcome"]; changed {
					t.Fatal("observation mutated the caller's data")
				}
				if count := pipeline.Counters().Get(sess.ID, rule.Qualified(), oar.CounterFire); count != 1 {
					t.Fatalf("output refusal evaluated %d times", count)
				}
			})
		}
	}
}

func TestOutputSpillRefusalReturnsObservationWithoutClaimingASpill(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools: ToolsDeps{
			DataDir: t.TempDir(),
		},
	})
	content := `{"truncated":true,"value":"` + strings.Repeat("x", 2048) + `"}`
	got := loop.Tools.truncateToolResultForSession(t.Context(), "command", toolResultStorageProjection{content: content}, content, 128, 1024, &api.Session{ID: "session", ProjectID: "project"})
	if got.reject == nil || got.reject.Code != tooloutput.ToolOutputSpillCapExceededCode {
		t.Fatalf("spill observation = %+v", got)
	}
	if got.content != "" || got.limit.Spilled || got.facts.PrimaryCode() != "" {
		t.Fatalf("unadmitted spill claimed delivery before occurrence evaluation: %+v", got)
	}
}
