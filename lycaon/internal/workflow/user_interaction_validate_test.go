package workflow

import (
	"strings"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestValidateUserInteractionGatesRejectsMismatchedFeedbackPhase(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "bad-feedback",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "stub",
			CompleteWhen: "user_feedback_received:clarify",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which API?"},
			},
		}},
	})
	errs := ValidateUserInteractionGates(m)
	if len(errs) == 0 {
		t.Fatal("expected validation errors")
	}
	found := false
	for _, e := range errs {
		if e.Code == "feedback_gate_phase_mismatch" || e.Code == "feedback_gate_phase_missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestValidateUserInteractionGatesAcceptsHitlConsultedWithoutOnEnter(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "freeform-hitl",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "intake",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"hitl_consulted:intake"},
				Next:         "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	if errs := ValidateUserInteractionGates(m); len(errs) != 0 {
		t.Fatalf("hitl_consulted must not require on-enter feedback: %v", errs)
	}
}

func TestValidateUserInteractionGatesAcceptsAlignedFeedbackPhase(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "good-feedback",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "clarify",
			CompleteWhen: "user_feedback_received:clarify",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which API?"},
			},
		}},
	})
	if errs := ValidateUserInteractionGates(m); len(errs) != 0 {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestValidateUserInteractionGatesRejectsDecisionMismatch(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "bad-decision",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "confirm",
			CompleteWhen: "user_decision:other,yes",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{
					Prompt:       "Proceed?",
					ResponseType: workflowdef.FeedbackResponseSingleChoice,
					Options:      []string{"yes", "no"},
				},
			},
		}},
	})
	errs := ValidateUserInteractionGates(m)
	if len(errs) == 0 {
		t.Fatal("expected validation errors")
	}
	if !strings.Contains(errs[0].Code, "decision_gate") {
		t.Fatalf("errs = %+v", errs)
	}
}
