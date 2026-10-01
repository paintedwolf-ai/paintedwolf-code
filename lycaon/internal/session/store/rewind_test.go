package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type rewindTestStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	AppendMessages(context.Context, string, ...api.Message) error
	PrepareRewind(context.Context, RewindOperation) error
	SetRewindPhase(context.Context, string, string, string) error
	CommitRewind(context.Context, string, string, string, api.RewindSessionResponse) (int, error)
	GetRewindOperation(context.Context, string) (*RewindOperation, error)
	RewindOperationsForRecovery(context.Context) ([]RewindOperation, error)
	RewindOperationsForRecoverySession(context.Context, string) ([]RewindOperation, error)
	CommittedRewindOperationsForSweep(context.Context) ([]RewindOperationSweepItem, error)
	DeleteRewindOperation(context.Context, string, time.Time) error
}

// commitFixtureRewind follows the transaction phases with inert filesystem paths.
func commitFixtureRewind(t *testing.T, ctx context.Context, st rewindTestStore, sessionID, anchorID string) string {
	t.Helper()
	opID := uuid.NewString()
	testutil.FailErr(t, "prepare rewind", st.PrepareRewind(ctx, RewindOperation{
		ID: opID, SessionID: sessionID, AnchorMessageID: anchorID,
		InputDigest: "digest", ProjectDir: "/project", JournalPath: "/journal.json",
		CheckpointAnchorIDs: []string{anchorID},
	}))
	testutil.FailErr(t, "phase applying", st.SetRewindPhase(ctx, opID, "applying", ""))
	testutil.FailErr(t, "phase files_applied", st.SetRewindPhase(ctx, opID, "files_applied", ""))
	_, err := st.CommitRewind(ctx, opID, sessionID, anchorID, api.RewindSessionResponse{})
	testutil.FailErr(t, "commit rewind", err)
	return opID
}

// Committed receipts remain replayable until retention expires and are excluded from pending recovery.
func TestCommittedRewindSweepAndRetentionParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) rewindTestStore
	}{
		{name: "memory", open: func(*testing.T) rewindTestStore { return NewMemory() }},
		{name: "sql", open: func(t *testing.T) rewindTestStore { return openTurnSQLStore(t).(*SQL) }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			anchor := api.Message{
				ID: "anchor-1", Role: api.MessageRoleUser, Content: "hi",
				Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
				TrustTier: api.ContentTrustTierTrusted,
			}
			testutil.FailErr(t, "append anchor", st.AppendMessages(ctx, sess.ID, anchor))

			opID := commitFixtureRewind(t, ctx, st, sess.ID, anchor.ID)

			// Committed operations stay out of the recovery listing: boot re-stats
			// every journal path in it, so it tracks in-flight rewinds, not history.
			hot, err := st.RewindOperationsForRecovery(ctx)
			testutil.FailErr(t, "recovery listing", err)
			for _, op := range hot {
				if op.ID == opID {
					t.Fatalf("committed operation %s appeared in the hot recovery listing", opID)
				}
			}
			hotForSession, err := st.RewindOperationsForRecoverySession(ctx, sess.ID)
			testutil.FailErr(t, "recovery listing for session", err)
			for _, op := range hotForSession {
				if op.ID == opID {
					t.Fatalf("committed operation %s appeared in the session-scoped recovery listing", opID)
				}
			}

			swept, err := st.CommittedRewindOperationsForSweep(ctx)
			testutil.FailErr(t, "sweep listing", err)
			var found *RewindOperationSweepItem
			for i := range swept {
				if swept[i].ID == opID {
					found = &swept[i]
				}
			}
			if found == nil {
				t.Fatalf("sweep listing = %+v, want operation %s", swept, opID)
			}
			if len(found.CheckpointAnchorIDs) != 1 || found.CheckpointAnchorIDs[0] != anchor.ID {
				t.Fatalf("CheckpointAnchorIDs = %+v, want [%s]", found.CheckpointAnchorIDs, anchor.ID)
			}

			// Not yet past retention: the row survives so an operation-id retry
			// within the window can still replay its cached receipt.
			testutil.FailErr(t, "delete before cutoff", st.DeleteRewindOperation(ctx, opID, found.CreatedAt))
			stillThere, err := st.GetRewindOperation(ctx, opID)
			testutil.FailErr(t, "get after no-op delete", err)
			if stillThere == nil || stillThere.Status != "committed" {
				t.Fatalf("operation = %+v, want to survive a delete call at its own created_at", stillThere)
			}

			// Past retention: the caller-supplied cutoff, not any fixed
			// duration, decides when the row ages out.
			testutil.FailErr(t, "delete past cutoff", st.DeleteRewindOperation(ctx, opID, found.CreatedAt.Add(time.Second)))
			gone, err := st.GetRewindOperation(ctx, opID)
			testutil.FailErr(t, "get after delete", err)
			if gone != nil {
				t.Fatalf("operation = %+v, want deleted past retention", gone)
			}
		})
	}
}

func TestCheckpointAnchorIDsRequireAnArrayOfIdentities(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `[null]`, `[""]`, `[" "]`, `[1]`, `[] {}`} {
		if ids, err := decodeCheckpointAnchorIDs(raw); err == nil {
			t.Errorf("accepted malformed anchors %q: %v", raw, ids)
		}
	}
	for _, ids := range [][]string{nil, {}, {"anchor-1", "anchor-2"}} {
		raw, err := encodeCheckpointAnchorIDs(ids)
		testutil.FailErr(t, "encode anchors", err)
		decoded, err := decodeCheckpointAnchorIDs(raw)
		testutil.FailErr(t, "decode anchors", err)
		if len(decoded) != len(ids) {
			t.Fatalf("anchors = %v, want %v", decoded, ids)
		}
		for i := range ids {
			if decoded[i] != ids[i] {
				t.Fatalf("anchor %d = %q, want %q", i, decoded[i], ids[i])
			}
		}
	}
}
