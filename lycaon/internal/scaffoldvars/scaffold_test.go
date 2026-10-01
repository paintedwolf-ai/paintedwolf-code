package scaffoldvars

import "testing"

func TestHasPendingUserInputRecognizesFeedbackAndDecision(t *testing.T) {
	for _, bucket := range []string{"user_feedback", "user_decision"} {
		t.Run(bucket, func(t *testing.T) {
			vars := map[string]any{
				bucket: map[string]any{"review": map[string]any{"pending": true}},
			}
			if !HasPendingUserInput(vars) {
				t.Fatalf("%s was not recognized as pending", bucket)
			}
		})
	}
}

func TestHumanApprovalAwaitingRequiresReady(t *testing.T) {
	vars := map[string]any{"human_approval": map[string]any{"active": true, "blueprint_path": "bp.md"}}
	if HumanApprovalAwaiting(vars) {
		t.Fatal("active without ready must not await")
	}
	vars["human_approval"].(map[string]any)["ready"] = true
	if !HumanApprovalAwaiting(vars) {
		t.Fatal("active + ready must await")
	}
}

func TestHumanApprovalAwaitingRequiresDocument(t *testing.T) {
	vars := map[string]any{"human_approval": map[string]any{
		"active": true, "ready": true,
	}}
	if HumanApprovalAwaiting(vars) {
		t.Fatal("approval without a bound document must not await")
	}
}

func TestHumanApprovalAwaitingIssuedWithHashIsComplete(t *testing.T) {
	vars := map[string]any{"human_approval": map[string]any{
		"active": true, "ready": true, "issued": true,
		"blueprint_path": "bp.md", "blueprint_hash": "abc",
	}}
	if HumanApprovalAwaiting(vars) {
		t.Fatal("issued with a content hash is complete")
	}
}

func TestHumanApprovalAwaitingIssuedWithoutHashStillAwaits(t *testing.T) {
	vars := map[string]any{"human_approval": map[string]any{
		"active": true, "ready": true, "issued": true,
		"blueprint_path": "bp.md",
	}}
	if !HumanApprovalAwaiting(vars) {
		t.Fatal("issued without a hash still awaits")
	}
}
