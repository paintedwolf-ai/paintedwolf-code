package validation

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestTerminalVerdictRequiresExactStampedClaimIDs(t *testing.T) {
	def := workflowdef.ReviewLoopDef{ReconcilesPhase: "claims", VerdictSchema: map[string]string{"verdict": "CHALLENGED|NEEDS_INVESTIGATION", "challenges": workflowdef.VerdictClaimsType}, ClaimStatuses: map[string]workflowdef.ClaimClass{"survives": workflowdef.ClaimHeld}}
	rules := VerdictRules{KnownClaims: map[string]bool{"secret-gates-hold": true, "api-auth-holds": true}}
	for _, followups := range []int{0, 2} {
		def.FollowupAttempts = followups
		for _, id := range []string{"secret-gates-holds", "unrelated"} {
			raw, err := json.Marshal([]VerdictClaim{{ID: id, Title: "New claim", Statement: "Supported conclusion", Status: "survives"}})
			testutil.FailErr(t, "encode challenges", err)
			verdict := map[string]string{"verdict": "CHALLENGED", "challenges": string(raw)}
			rejection := toolrejection.AsToolReject(ValidateReviewLoopVerdict(def, verdict, rules))
			if rejection == nil || rejection.Code != ReviewLoopVerdictInvalidCode || rejection.Data["reason"] != "claim_outcome_required" {
				t.Fatalf("missing stamped claim misclassified: %+v", rejection)
			}
			want := []string{"api-auth-holds", "secret-gates-hold"}
			if !slices.Equal(rejection.Data["missing_claim_ids"].([]string), want) || !slices.Equal(rejection.Data["expected_claim_ids"].([]string), want) {
				t.Fatalf("unstable claim repair facts: %+v", rejection.Data)
			}
			if _, fabricated := rejection.Data["question_id"]; fabricated {
				t.Fatal("missing claim manufactured a question identity")
			}
			verdict["verdict"] = "NEEDS_INVESTIGATION"
			testutil.FailErr(t, "nonterminal verdict may leave stamped outcomes pending", ValidateReviewLoopVerdict(def, verdict, rules))
		}
	}
}

func TestTerminalVerdictAcceptsCorrectedClaimsAcrossFields(t *testing.T) {
	def := workflowdef.ReviewLoopDef{ReconcilesPhase: "claims", VerdictSchema: map[string]string{"verdict": "CHALLENGED", "challenges": workflowdef.VerdictClaimsType, "advisories": workflowdef.VerdictClaimsType}, ClaimStatuses: map[string]workflowdef.ClaimClass{"survives": workflowdef.ClaimHeld}}
	rules := VerdictRules{KnownClaims: map[string]bool{"secret-gates-hold": true, "api-auth-holds": true}}
	verdict := map[string]string{"verdict": "CHALLENGED", "challenges": `[{"id":"secret-gates-hold","statement":"Gates verified","status":"survives"}]`, "advisories": `[{"id":"api-auth-holds","statement":"Auth verified","status":"survives"}]`}
	testutil.FailErr(t, "accept exact stamped identities across claim fields", ValidateReviewLoopVerdict(def, verdict, rules))
	verdict["advisories"] = "[]"
	rejection := toolrejection.AsToolReject(ValidateReviewLoopVerdict(def, verdict, rules))
	if rejection == nil || !slices.Equal(rejection.Data["missing_claim_ids"].([]string), []string{"api-auth-holds"}) {
		t.Fatalf("missing outcome = %+v", rejection)
	}
}

func TestIndependentReviewDoesNotRequireRepeatingPriorClaims(t *testing.T) {
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "ACCEPTED", "claims": workflowdef.VerdictClaimsType}}
	rules := VerdictRules{KnownClaims: map[string]bool{"prior-claim": true}}
	testutil.FailErr(t, "independent review may introduce different claims", ValidateReviewLoopVerdict(def, map[string]string{"verdict": "ACCEPTED", "claims": "[]"}, rules))
}
