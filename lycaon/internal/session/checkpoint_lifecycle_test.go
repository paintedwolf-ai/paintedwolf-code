package session

import (
	"errors"
	"testing"

	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Orphan checkpoints retain file copies without a live session.
func TestRemoveOrphanCheckpointsKeepsLiveSessionsOnly(t *testing.T) {
	// Memory storage permits an orphan fixture; SQL rejects its publication.
	mgr, sessions := newTestManager(t)
	dir := t.TempDir()
	ctx := t.Context()
	live, err := sessions.Create(ctx, api.CreateSessionRequest{}, "project")
	testutil.FailErr(t, "create live session", err)
	liveSessionID := live.ID

	store := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	_, err = store.Open(t.Context(), liveSessionID, "anchor-live")
	testutil.FailErr(t, "open live anchor", err)
	_, err = store.Open(t.Context(), "deleted-session", "anchor-dead")
	testutil.FailErr(t, "open orphan anchor", err)

	if removed := mgr.Chats.Captures.RemoveOrphanCheckpoints(ctx, dir); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := store.Load(t.Context(), liveSessionID, "anchor-live"); err != nil {
		t.Fatalf("removed a checkpoint whose session is still live: %v", err)
	}
	if _, err := store.Load(t.Context(), "deleted-session", "anchor-dead"); !errors.Is(err, sessionstore.ErrCheckpointMissing) {
		t.Fatal("orphan checkpoint survived cleanup")
	}
}

func TestDeleteSessionRemovesCheckpoints(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	checkpoint := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	_, err := checkpoint.Open(t.Context(), sessionID, "anchor")
	testutil.FailErr(t, "open checkpoint", err)

	testutil.FailErr(t, "delete session", mgr.Chats.Delete(t.Context(), sessionID))
	if _, err := checkpoint.Load(t.Context(), sessionID, "anchor"); !errors.Is(err, sessionstore.ErrCheckpointMissing) {
		t.Fatalf("checkpoint survived session deletion: %v", err)
	}
}
