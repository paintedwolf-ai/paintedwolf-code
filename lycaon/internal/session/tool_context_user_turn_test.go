package session

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func userTurnMessage(role api.MessageRole, visibility api.MessageVisibility) api.Message {
	return api.Message{
		ID:         uuid.NewString(),
		Role:       role,
		Content:    "x",
		Origin:     api.MessageOriginUser,
		Authority:  api.ContentAuthorityUser,
		TrustTier:  api.ContentTrustTierTrusted,
		Visibility: visibility,
		CreatedAt:  time.Now().UTC(),
	}
}

// Writes and client turn lenses share the permanent opener ordinal across host continuations.
func TestToolContextUserTurnTracksSessionCurrentTurn(t *testing.T) {
	ctx := t.Context()
	sessions := store.NewMemory()
	mgr := NewHost(sessions, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)

	sess, err := sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	assertTurn := func(step string, want int) {
		t.Helper()
		tctx, err := mgr.ToolContext.Build(ctx, sess, tools.DefaultToolProfileID, inject.Machine{})
		if err != nil {
			t.Fatalf("%s: buildToolContext: %v", step, err)
		}
		if tctx.UserTurn != want {
			t.Fatalf("%s: ToolContext.UserTurn = %d, want %d", step, tctx.UserTurn, want)
		}
		if tctx.SourceWorkspaceKind != api.SourceWorkspaceKindProject {
			t.Fatalf("%s: workspace kind = %q, want project", step, tctx.SourceWorkspaceKind)
		}
		if tctx.MaxToolSpillBytes != mgr.Limits.Effective(ctx, sess).MaxToolSpillBytes {
			t.Fatal("tool recovery reads must use the session's spill retention bound")
		}
		hydrated, err := sessions.Get(ctx, sess.ID)
		if err != nil {
			t.Fatalf("%s: get session: %v", step, err)
		}
		if hydrated.CurrentTurn != tctx.UserTurn {
			t.Fatalf("%s: Session.current_turn = %d, ToolContext.UserTurn = %d",
				step, hydrated.CurrentTurn, tctx.UserTurn)
		}
	}

	assertTurn("before any prompt", 0)

	if err := sessions.AppendMessages(ctx, sess.ID,
		userTurnMessage(api.MessageRoleUser, api.MessageVisibilityTranscript)); err != nil {
		t.Fatalf("append first user turn: %v", err)
	}
	assertTurn("first user turn", 1)

	// A host kick and the assistant's own reply are not new turns.
	if err := sessions.AppendMessages(ctx, sess.ID,
		userTurnMessage(api.MessageRoleUser, api.MessageVisibilityInternal),
		userTurnMessage(api.MessageRoleAssistant, api.MessageVisibilityTranscript)); err != nil {
		t.Fatalf("append host rows: %v", err)
	}
	assertTurn("after a host kick", 1)

	if err := sessions.AppendMessages(ctx, sess.ID,
		userTurnMessage(api.MessageRoleUser, api.MessageVisibilityTranscript)); err != nil {
		t.Fatalf("append second user turn: %v", err)
	}
	assertTurn("second user turn", 2)
}
