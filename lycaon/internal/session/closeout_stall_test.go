package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/limits"
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

func TestCloseoutStallFuseTrips(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	ctx := context.Background()

	if mgr.NoteCoordinatorToolTurn(ctx, sess.ID) {
		t.Fatal("fuse must not trip with no reject outstanding (disarmed)")
	}

	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestCitationsRequiredCode, guidance.InvestCitationsRequiredCode, "draft", nil)
	budget := limits.DefaultCloseoutStallToolTurns
	for i := 1; i < budget; i++ {
		if mgr.NoteCoordinatorToolTurn(ctx, sess.ID) {
			t.Fatalf("fuse tripped early at tool turn %d, budget %d", i, budget)
		}
	}
	if !mgr.NoteCoordinatorToolTurn(ctx, sess.ID) {
		t.Fatalf("fuse did not trip at the budget of %d tool turns", budget)
	}
}

func TestCloseoutStallRejectResetsToolTurns(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	ctx := context.Background()

	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestCitationsRequiredCode, guidance.InvestCitationsRequiredCode, "draft", nil)
	mgr.NoteCoordinatorToolTurn(ctx, sess.ID)
	mgr.NoteCloseoutGroundingReject(ctx, sess.ID, guidance.InvestCitationsRequiredCode, guidance.InvestCitationsRequiredCode, "draft", nil)

	budget := limits.DefaultCloseoutStallToolTurns
	for i := 1; i < budget; i++ {
		if mgr.NoteCoordinatorToolTurn(ctx, sess.ID) {
			t.Fatalf("fuse tripped early after reset at tool turn %d", i)
		}
	}
	if !mgr.NoteCoordinatorToolTurn(ctx, sess.ID) {
		t.Fatal("fuse did not trip at budget after a re-arming reject")
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
	mgr.beginCloseoutIntent(ctx, sess.ID)
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
