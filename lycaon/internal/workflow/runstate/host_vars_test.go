package runstate_test

import (
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"
	"time"
)

// The approval wait is stamped when it opens, kept while it stays open, dropped
// when it ends, and stamped afresh when a blueprint edit reopens it.
func TestHumanApprovalAwaitingSinceFollowsTheWait(t *testing.T) {
	vars := runstate.StampHumanApprovalPhase(map[string]any{}, &workflowdef.HumanApprovalConfig{}, "blueprints/plan.md")
	if _, ok := scaffoldvars.HumanApprovalAwaitingSince(vars); ok {
		t.Fatal("stamped before approval prerequisites were ready")
	}
	vars = runstate.SetHumanApprovalReady(vars, true)
	opened, ok := scaffoldvars.HumanApprovalAwaitingSince(vars)
	if !ok {
		t.Fatal("no stamp when the approval wait opened")
	}

	time.Sleep(2 * time.Millisecond)
	vars = runstate.SetHumanApprovalReady(vars, true)
	vars = runstate.SetHumanApprovalBlueprintPath(vars, "blueprints/moved.md")
	if kept, ok := scaffoldvars.HumanApprovalAwaitingSince(vars); !ok || !kept.Equal(opened) {
		t.Fatalf("writes inside the wait moved its stamp: %v -> %v", opened, kept)
	}

	vars = runstate.SetHumanApprovalIssued(vars, true)
	vars = runstate.SetHumanApprovalHash(vars, "sha256:approved")
	if _, ok := scaffoldvars.HumanApprovalAwaitingSince(vars); ok {
		t.Fatal("stamp survived the approval")
	}

	time.Sleep(2 * time.Millisecond)
	vars = runstate.ClearHumanApprovalHash(vars)
	reopened, ok := scaffoldvars.HumanApprovalAwaitingSince(vars)
	if !ok || !reopened.After(opened) {
		t.Fatalf("reopened wait stamped %v, want after %v", reopened, opened)
	}
}

// A phase's feedback or decision request ages from when the phase asked.
func TestPendingInputSinceIsTheRequestTime(t *testing.T) {
	before := time.Now().UTC()
	vars := runstate.SetFeedbackPending(map[string]any{}, "review", "Approve this?")
	since, ok := runstate.PendingInputSince(vars)
	if !ok || since.Before(before) || since.After(time.Now().UTC()) {
		t.Fatalf("feedback since = %v (ok=%v), want the request time", since, ok)
	}

	before = time.Now().UTC()
	vars = runstate.SetDecisionPending(map[string]any{}, "choose", "Which?", []string{"a", "b"})
	since, ok = runstate.PendingInputSince(vars)
	if !ok || since.Before(before) || since.After(time.Now().UTC()) {
		t.Fatalf("decision since = %v (ok=%v), want the request time", since, ok)
	}
}
