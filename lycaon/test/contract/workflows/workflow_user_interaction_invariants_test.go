package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestBundledManifestsUserInteractionGatesAligned(t *testing.T) {
	t.Parallel()
	for key, m := range workflowfixture.ContractAllResolvedManifests(t) {
		errs := workflow.ValidateUserInteractionGates(m)
		if len(errs) > 0 {
			t.Fatalf("manifest %q: %+v", key, errs)
		}
	}
}

func TestComposeValidatorRejectsFeedbackGatePhaseMismatch(t *testing.T) {
	t.Parallel()
	c := workflowfixture.ContractWorkflowComposer(t)
	manifest := `id: mismatch-session
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Understanding the request
    on_enter:
      request_user_feedback:
        prompt: "Which API?"
    complete_when: user_feedback_received:clarify
    next: build
  - id: build
    activity_label: Building the change
    on_enter:
      set_posture: build
    complete_when: delegation_closeout_complete
`
	_, err := c.Compose(t.Context(), workflow.ComposeRequest{
		SessionID:    "contract-mismatch",
		ManifestYAML: []byte(manifest),
		CreatedBy:    "coordinator",
	})
	if err == nil {
		t.Fatal("expected compose validation failure")
	}
}
