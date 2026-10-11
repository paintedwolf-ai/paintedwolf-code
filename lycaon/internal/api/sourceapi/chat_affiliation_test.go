package sourceapi

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// The focused chat stays the change's affiliation whether or not a turn is
// running; the host names the turn only while one is active.
func TestUserSourceChatAffiliationNamesTheChatAndOnlyAnActiveTurn(t *testing.T) {
	ctx := t.Context()
	sessions := store.NewMemory()
	sess, err := sessions.Create(ctx, wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	server := &Handler{Workspace: &Workspace{SessionStore: sessions}}
	request := httptest.NewRequest("POST", "/source/rename?session_id="+sess.ID, nil)

	assertAffiliation := func(step, wantSession string, wantTurn int) {
		t.Helper()
		gotSession, gotTurn := server.Workspace.UserSourceChatAffiliation(request)
		if gotSession != wantSession || gotTurn != wantTurn {
			t.Fatalf("%s affiliation = (%q, %d), want (%q, %d)",
				step, gotSession, gotTurn, wantSession, wantTurn)
		}
	}

	assertAffiliation("idle before a prompt", sess.ID, 0)
	testutil.FailErr(t, "mark busy before prompt", sessions.SetSessionStatus(ctx, sess.ID, wire.SessionStatusBusy))
	assertAffiliation("busy before a prompt", sess.ID, 0)
	testutil.FailErr(t, "append user prompt", sessions.AppendMessages(ctx, sess.ID, wire.Message{
		ID: "user-1", Role: wire.MessageRoleUser, Content: "inspect this",
		Origin: wire.MessageOriginUser, Authority: wire.ContentAuthorityUser,
		TrustTier: wire.ContentTrustTierTrusted, Visibility: wire.MessageVisibilityTranscript,
		CreatedAt: time.Now().UTC(),
	}))
	assertAffiliation("active turn", sess.ID, 1)
	testutil.FailErr(t, "settle turn", sessions.SetSessionStatus(ctx, sess.ID, wire.SessionStatusIdle))
	assertAffiliation("idle after turn", sess.ID, 0)

	// A chat the host cannot place leaves the change unaffiliated rather than refusing it.
	gone := httptest.NewRequest("POST", "/source/rename?session_id=00000000-0000-4000-8000-000000000000", nil)
	if gotSession, gotTurn := server.Workspace.UserSourceChatAffiliation(gone); gotSession != "" || gotTurn != 0 {
		t.Fatalf("unknown chat affiliation = (%q, %d), want none", gotSession, gotTurn)
	}
	if session, turn, err := requestscope.ChatAffiliation(ctx, server.Workspace.SessionStore, "another-project", sess.ID); err != nil || session != "" || turn != 0 {
		t.Fatalf("another project's chat affiliation = (%q, %d, %v), want none", session, turn, err)
	}
}
