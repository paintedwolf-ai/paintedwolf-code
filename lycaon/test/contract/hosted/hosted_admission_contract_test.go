package contract

import (
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const ciGateStep = "python3 scripts/ci_verification.py gate"

// Drafts spend no verification runners, never pass the required check, and get
// the fast tier once marked ready.
func TestDraftAndDiagnosticRunsCannotSatisfyTheRequiredCheck(t *testing.T) {
	t.Parallel()
	workflow := hostedWorkflowFile(t, "ci")
	var pullRequest struct{ Types []string }
	trigger := workflow.On["pull_request"]
	contractcheck.FailErr(t, "decode pull_request trigger", trigger.Decode(&pullRequest))
	types := slices.Clone(pullRequest.Types)
	slices.Sort(types)
	if !slices.Equal(types, []string{"opened", "ready_for_review", "reopened", "synchronize"}) {
		t.Errorf("pull request CI must run when a ready pull request changes and when a draft becomes ready, got %v", pullRequest.Types)
	}
	if !strings.Contains(workflow.Jobs["fast"].If, "!github.event.pull_request.draft || github.event.action == 'ready_for_review'") {
		t.Error("draft pull requests must wait unless the event marks them ready")
	}
	const draftGroup = "-${{ github.event.pull_request.draft && github.event.action != 'ready_for_review' && 'draft' || 'ready' }}"
	if !strings.Contains(workflow.Concurrency.Group, draftGroup) {
		t.Error("draft events must use a separate concurrency group so they cannot cancel ready verification")
	}
	const checkName = "${{ inputs.lane != '' && 'diagnostic-summary' || (github.event.pull_request.draft == true && github.event.action != 'ready_for_review' && 'draft-check' || 'check') }}"
	if workflow.Jobs["check"].Name != checkName {
		t.Error("skipped draft and diagnostic aggregates must not satisfy the required check")
	}
	refused := false
	for _, step := range workflow.Jobs["check"].Steps {
		refused = refused || strings.HasPrefix(step.Run, ciGateStep) &&
			step.Env["DRAFT"] == "${{ github.event.pull_request.draft == true && github.event.action != 'ready_for_review' }}" && strings.Contains(step.Run, `--draft "$DRAFT"`)
	}
	if !refused {
		t.Error("the required check must refuse a draft rather than pass on skipped verification")
	}
}
