package store

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type rewindStore interface {
	PrepareRewind(context.Context, RewindOperation) error
	SetRewindPhase(context.Context, string, string, string) error
	CommitRewind(context.Context, string, string, string, api.RewindSessionResponse) (int, error)
}

// rewindFrom truncates a session from anchor through the committed rewind
// path and returns how many messages it removed.
func rewindFrom(t *testing.T, st rewindStore, sessionID, anchor string) int {
	t.Helper()
	id := uuid.NewString()
	testutil.FailErr(t, "prepare rewind", st.PrepareRewind(t.Context(), RewindOperation{ID: id, SessionID: sessionID, AnchorMessageID: anchor, InputDigest: "test"}))
	testutil.FailErr(t, "apply rewind files", st.SetRewindPhase(t.Context(), id, "files_applied", ""))
	removed, err := st.CommitRewind(t.Context(), id, sessionID, anchor, api.RewindSessionResponse{})
	testutil.FailErr(t, "commit rewind", err)
	return removed
}
