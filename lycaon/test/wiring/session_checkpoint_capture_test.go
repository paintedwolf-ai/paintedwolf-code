package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func readCheckpointManifest(t *testing.T, stateRoot, projectDir, sessionID, anchorID string, refs sessionstore.CheckpointRepository) sessioncheckpoint.Manifest {
	t.Helper()
	store := sessioncheckpoint.New(stateRoot, projectDir, refs)
	if store == nil {
		t.Fatal("checkpoint store not configured")
	}
	manifest, err := store.Load(t.Context(), sessionID, anchorID)
	testutil.FailErr(t, "load checkpoint manifest", err)
	return *manifest
}

func visibleUserMessageIDs(t *testing.T, msgs []api.Message) []string {
	t.Helper()
	var out []string
	for _, m := range msgs {
		if m.Role == api.MessageRoleUser && m.Visibility != api.MessageVisibilityInternal {
			out = append(out, m.ID)
		}
	}
	return out
}

func TestPromptSealsARewindAnchorInAProductionBuild(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: "first ask", FollowUpText: MockCoordinatorCloseoutJSON("Done with the first ask.", "")},
		{Pattern: "second ask", FollowUpText: MockCoordinatorCloseoutJSON("Done with the second ask.", "")},
	}})
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	projectDir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, projectDir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, projectDir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	if _, err := h.SessionMgr.Prompt(ctx, sess.ID, "first ask"); err != nil {
		testutil.FailErr(t, "first prompt", err)
	}
	if _, err := h.SessionMgr.Prompt(ctx, sess.ID, "second ask"); err != nil {
		testutil.FailErr(t, "second prompt", err)
	}

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	anchors := visibleUserMessageIDs(t, msgs)
	if len(anchors) != 2 {
		t.Fatalf("visible user messages = %d, want 2", len(anchors))
	}
	for _, anchorID := range anchors {
		man := readCheckpointManifest(t, h.testDir, projectDir, sess.ID, anchorID, h.Store)
		if man.AnchorMessageID != anchorID {
			t.Fatalf("manifest anchor = %q, want %q", man.AnchorMessageID, anchorID)
		}
		if man.SessionID != sess.ID {
			t.Fatalf("manifest session = %q, want %q", man.SessionID, sess.ID)
		}
		// Checkpoints exclude engine state.
		for rel := range man.Paths {
			if rel == settingsoverlay.DirName() || strings.HasPrefix(rel, settingsoverlay.DirName()+"/") {
				t.Fatalf("checkpoint captured engine state at %q", rel)
			}
		}
	}
}
