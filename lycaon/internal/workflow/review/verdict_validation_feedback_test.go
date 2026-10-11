package review

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"slices"
	"testing"

	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
)

func TestVerdictInvalidDetailsPreservesClaimRepairFacts(t *testing.T) {
	missing := []string{"secret-gates-hold"}
	err := errors.Join(&toolrejection.ToolReject{Code: workflowvalidation.ReviewLoopVerdictInvalidCode, Data: map[string]any{
		"reason": "claim_outcome_required", "missing_claim_ids": missing, "expected_claim_ids": missing,
	}}, errors.New("another invalid field"))
	data := verdictInvalidDetails("{verdict: CHALLENGED}", err)
	if data["reason"] != "claim_outcome_required" || !slices.Equal(data["missing_claim_ids"].([]string), missing) || !slices.Equal(data["expected_claim_ids"].([]string), missing) {
		t.Fatalf("lost structured repair: %+v", data)
	}
	if data["expected_call"] != describeVerdictCall("{verdict: CHALLENGED}") || data["issues"] != err.Error() {
		t.Fatalf("lost schema or independent issues: %+v", data)
	}
	ordinary := verdictInvalidDetails("{}", errors.New("wrong field"))
	if ordinary["reason"] != "wrong field" {
		t.Fatalf("lost ordinary error: %+v", ordinary)
	}
}
