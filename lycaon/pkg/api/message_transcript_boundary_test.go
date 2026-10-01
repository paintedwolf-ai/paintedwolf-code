package api

import "testing"

func TestUserIntentBoundary_visibleUserOnly(t *testing.T) {
	if got := UserIntentBoundary([]Message{{Role: MessageRoleUser, Content: "fix auth"}}); got != 1 {
		t.Fatalf("UserIntentBoundary = %d want 1", got)
	}
}

func TestUserIntentBoundary_skipsInternalScheduledKick(t *testing.T) {
	history := []Message{
		{Role: MessageRoleUser, Content: "run the experiment"},
		{Role: MessageRoleAssistant, Content: "dispatching verifier"},
		{
			Role:       MessageRoleUser,
			Content:    "Scheduled wake — your wait() timer trigger fired.",
			Visibility: MessageVisibilityInternal,
		},
	}
	if got := UserIntentBoundary(history); got != 1 {
		t.Fatalf("UserIntentBoundary = %d want 1 (anchor on visible user, not scheduled kick)", got)
	}
}

func TestUserIntentBoundary_skipsInternalWorkerFinishedKick(t *testing.T) {
	visible := Message{Role: MessageRoleUser, Content: "implement auth"}
	internal := Message{
		Role:       MessageRoleUser,
		Content:    "Worker task finished — read envelope report_json first",
		Visibility: MessageVisibilityInternal,
	}
	if got := UserIntentBoundary([]Message{visible, internal}); got != 1 {
		t.Fatalf("UserIntentBoundary = %d want 1", got)
	}
}

func TestUserIntentBoundary_skipsWorkflowBoundary(t *testing.T) {
	history := []Message{
		{Role: MessageRoleUser, Content: "build game"},
		{Kind: MessageKindWorkflowBoundary, Role: MessageRoleSystem},
	}
	if got := UserIntentBoundary(history); got != 1 {
		t.Fatalf("UserIntentBoundary = %d want 1", got)
	}
}

func TestUserIntentBoundary_secondVisibleUser(t *testing.T) {
	history := []Message{
		{Role: MessageRoleUser, Content: "first ask"},
		{Role: MessageRoleAssistant, Content: "ok"},
		{Role: MessageRoleUser, Content: "follow up"},
	}
	if got := UserIntentBoundary(history); got != 3 {
		t.Fatalf("UserIntentBoundary = %d want 3", got)
	}
}

func TestUserIntentBoundary_skipsUserContinuation(t *testing.T) {
	history := []Message{
		{Role: MessageRoleUser, Origin: MessageOriginUser, Content: "first ask"},
		{Role: MessageRoleAssistant, Origin: MessageOriginModel, Content: "working"},
		{
			Role: MessageRoleUser, Origin: MessageOriginUser,
			Kind: MessageKindUserContinuation, Content: "adjust the approach",
		},
	}
	if got := UserIntentBoundary(history); got != 1 {
		t.Fatalf("UserIntentBoundary = %d want 1", got)
	}
	if !IsUserInstructionMessage(history[2]) {
		t.Fatal("user continuation must remain model-facing user instruction")
	}
}

func TestUserIntentBoundary_noVisibleUserReturnsZero(t *testing.T) {
	history := []Message{
		{
			Role:       MessageRoleUser,
			Content:    "Scheduled wake — timer",
			Visibility: MessageVisibilityInternal,
		},
		{Role: MessageRoleAssistant, Content: "working"},
	}
	if got := UserIntentBoundary(history); got != 0 {
		t.Fatalf("UserIntentBoundary = %d want 0", got)
	}
}
