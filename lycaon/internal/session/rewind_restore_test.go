package session

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Rewind requires the checkpoint state root.
func TestRewindRefusesWhenNoCheckpointRootIsConfigured(t *testing.T) {
	mgr, sessionID, _ := newCheckpointTestSession(t)
	mgr.SetDataDir("")
	ctx := context.Background()

	anchor := api.Message{
		ID: "u-unsealed", Role: api.MessageRoleUser, Content: "change something",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
	}
	testutil.FailErr(t, "append", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(ctx, sessionID, anchor))

	if _, err := rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor.ID); !errors.Is(err, checkpointcontrol.ErrCheckpointRootUnset) {
		t.Fatalf("RewindToPrompt err = %v, want checkpointcontrol.ErrCheckpointRootUnset", err)
	}
	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	if _, ok := rewindFixtureHasMessage(msgs, anchor.ID); !ok {
		t.Fatal("a refused rewind must leave the transcript intact")
	}
}

func TestRewindOfTranscriptOnlyTurnNeedsNoFileCheckpoint(t *testing.T) {
	mgr, id, _ := newCheckpointTestSession(t)
	anchor := appendRewindAsk(t, mgr, id)
	result, err := rewindTest(t, mgr, t.Context(), uuid.NewString(), id, anchor)
	testutil.FailErr(t, "rewind transcript only turn", err)
	if result.TruncatedMessageCount != 1 || len(result.RestoredPaths) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestRewindRestoredPromptIsUserInstructionOnly(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()

	anchor := api.Message{
		ID:        "u-attach",
		Role:      api.MessageRoleUser,
		Content:   "review this\n\n```attachment filename=\"main.go\" mime=\"text/plain\" truncated=\"false\"\n[User attached file: src/main.go]\n```",
		Origin:    api.MessageOriginUser,
		Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
		ContentParts: []api.MessageContentPart{
			{
				Content: "review this", Origin: api.MessageOriginUser,
				Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
			},
			{
				Content: "```attachment filename=\"main.go\" mime=\"text/plain\" truncated=\"false\"\n[User attached file: src/main.go]\n```",
				Origin:  api.MessageOriginRetrieval, Authority: api.ContentAuthorityNone,
				TrustTier: api.ContentTrustTierUntrusted,
				Source:    "src/main.go", MediaType: "text/plain",
			},
		},
		ArtifactIDs: []string{"art-1"},
	}
	testutil.FailErr(t, "append", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(ctx, sessionID, anchor))
	_, err := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store)).Open(t.Context(), sessionID, anchor.ID)
	testutil.FailErr(t, "open checkpoint", err)

	result, err := rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor.ID)
	testutil.FailErr(t, "rewind", err)
	if result.RestoredPrompt != "review this" {
		t.Fatalf("RestoredPrompt = %q, want prose only", result.RestoredPrompt)
	}
	if len(result.RestoredContentParts) != 2 {
		t.Fatalf("RestoredContentParts = %d, want 2", len(result.RestoredContentParts))
	}
	if len(result.RestoredArtifactIDs) != 1 || result.RestoredArtifactIDs[0] != "art-1" {
		t.Fatalf("RestoredArtifactIDs = %#v", result.RestoredArtifactIDs)
	}
}
