package closeouts

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
)

func TestCloseoutStallSeedsAndPersists(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	ctx := context.Background()

	attempt, prev := mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestCitationsRequiredCode, guidance.InvestCitationsRequiredCode, "The workflow system routes …", nil)
	if attempt != 1 || prev != "" {
		t.Fatalf("first reject attempt=%d prev=%q, want 1/empty", attempt, prev)
	}
	attempt, prev = mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestCitationsRequiredCode, guidance.InvestCitationsRequiredCode, "", nil)
	if attempt != 2 {
		t.Fatalf("second reject attempt=%d, want 2 (survives across runs)", attempt)
	}
	if prev != guidance.InvestCitationsRequiredCode {
		t.Fatalf("second reject prevKey=%q, want the first offender key", prev)
	}

	retained := mgr.CloseoutStallState(ctx, sess.ID)
	if !retained.Active || retained.Attempt != 2 {
		t.Fatalf("state active=%v attempt=%d, want true/2", retained.Active, retained.Attempt)
	}
	if retained.Drafted != "The workflow system routes …" {
		t.Fatalf("drafted synthesis not retained: %q", retained.Drafted)
	}
	if len(retained.ForcedBy) != 1 || retained.ForcedBy[0] != guidance.InvestCitationsRequiredCode {
		t.Fatalf("forcedBy=%v, want the single citation code", retained.ForcedBy)
	}
}

func TestCloseoutWorkRetiresDraftWithoutReplenishingBudgets(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	ctx := t.Context()
	mgr.RecordGroundingFriction(ctx, sess.ID)
	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestHandleNotObservedCode, "old", "old conclusion", nil)
	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.ReportClaimUnreportedCode, "document", "old conclusion", nil)
	promptBefore, cycleBefore := closeoutState(mgr, sess.ID, sess.ID)
	for i := 0; i < 7; i++ {
		mgr.ResumeWork(ctx, sess.ID)
		got := mgr.CloseoutStallState(ctx, sess.ID)
		if got.Active || got.Drafted != "" || got.PrevKey != "" || len(got.ForcedBy) != 0 || len(got.Unread) != 0 {
			t.Fatalf("tool turn %d retained old report eligibility: %+v", i, got)
		}
		promptAfter, cycleAfter := closeoutState(mgr, sess.ID, sess.ID)
		if got.Attempt != 1 || got.DocumentAttempt != 1 || promptAfter != promptBefore || cycleAfter.groundingRejects != cycleBefore.groundingRejects {
			t.Fatalf("tool turn %d replenished repair budgets: %+v", i, got)
		}
	}
	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestHandleNotObservedCode, "new", "fresh conclusion", nil)
	if got := mgr.CloseoutStallState(ctx, sess.ID); !got.Active || got.Drafted != "fresh conclusion" || got.Attempt != 2 {
		t.Fatalf("fresh conclusion did not replace retired draft: %+v", got)
	}
}

func TestCloseoutStallClearedOnCommitAndCycle(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	ctx := context.Background()

	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestCitationsRequiredCode, guidance.InvestCitationsRequiredCode, "draft", nil)
	mgr.ClearCloseoutStall(ctx, sess.ID)
	if mgr.CloseoutStallState(ctx, sess.ID).Active {
		t.Fatal("ClearCloseoutStall left the record active")
	}

	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestCitationsRequiredCode, guidance.InvestCitationsRequiredCode, "draft", nil)
	mgr.BeginIntent(ctx, sess.ID)
	if mgr.CloseoutStallState(ctx, sess.ID).Active {
		t.Fatal("cycle boundary did not drop the stall record")
	}
}

// Document refusals keep their own count, and the latest refused draft's
// unread members stay with the cycle until a refusal without them.
func TestCloseoutStallKeepsDocumentRepairApart(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	ctx := context.Background()
	unread := []jsonshape.Issue{{Path: "findings[0].ask", Pattern: "findings[].ask", Parent: "findings[]", Name: "ask", Kind: jsonshape.Unknown}}

	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.ReportFenceUnreadableCode, "k1", `{"synthesis":"Body."}`, unread)
	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestCitationsRequiredCode, "k2", "", nil)
	retained := mgr.CloseoutStallState(ctx, sess.ID)
	if retained.Attempt != 1 || retained.DocumentAttempt != 1 {
		t.Fatalf("attempts = %d citation, %d document; want 1 each", retained.Attempt, retained.DocumentAttempt)
	}
	if len(retained.Unread) != 0 {
		t.Fatalf("unread = %+v, want the latest refusal's (none)", retained.Unread)
	}
	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.ReportFenceUnreadableCode, "k3", "", unread)
	if got := mgr.CloseoutStallState(ctx, sess.ID); got.DocumentAttempt != 2 || len(got.Unread) != 1 {
		t.Fatalf("state = %+v, want the second document refusal and its unread member", got)
	}
}
