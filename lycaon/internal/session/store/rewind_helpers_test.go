package store

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// rewindFrom truncates a session from anchor through the committed rewind
// path and returns how many messages it removed.
func rewindFrom(t *testing.T, st *Repositories, sessionID, anchor string) int {
	t.Helper()
	id := uuid.NewString()
	testutil.FailErr(t, "prepare rewind", st.Rewind.PrepareRewind(t.Context(), RewindOperation{ID: id, SessionID: sessionID, AnchorMessageID: anchor, InputDigest: "test"}))
	testutil.FailErr(t, "apply rewind files", st.Rewind.SetRewindPhase(t.Context(), id, "files_applied", ""))
	removed, err := st.Rewind.CommitRewind(t.Context(), id, sessionID, anchor, api.RewindSessionResponse{})
	testutil.FailErr(t, "commit rewind", err)
	return removed
}
