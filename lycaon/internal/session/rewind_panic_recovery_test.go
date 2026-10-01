package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A partial apply panic rolls back within the same request.
func TestRewindApplyPanicRollsBackInPlace(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := checkpointCaller(t, mgr)

	anchor := api.Message{
		ID: "u-two-files", Role: api.MessageRoleUser, Content: "change two files",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
	}
	testutil.FailErr(t, "append", mgr.store.AppendMessages(ctx, sessionID, anchor))

	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	testutil.FailErr(t, "seed a", os.WriteFile(pathA, []byte("before-a"), 0o640))
	testutil.FailErr(t, "seed b", os.WriteFile(pathB, []byte("before-b"), 0o640))

	cpStore := sessioncheckpoint.New(mgr.dataDir, dir, mgr.store)
	_, err := cpStore.Open(t.Context(), sessionID, anchor.ID)
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture a", cpStore.CapturePreImage(t.Context(), sessionID, anchor.ID, "a.txt"))
	testutil.FailErr(t, "capture b", cpStore.CapturePreImage(t.Context(), sessionID, anchor.ID, "b.txt"))
	testutil.FailErr(t, "turn writes a", os.WriteFile(pathA, []byte("after-a"), 0o640))
	testutil.FailErr(t, "turn writes b", os.WriteFile(pathB, []byte("after-b"), 0o640))
	recordRewindTestEffect(t, mgr, sessionID, "a.txt", []byte("before-a"), []byte("after-a"), api.SourceChangeOpWrite)
	recordRewindTestEffect(t, mgr, sessionID, "b.txt", []byte("before-b"), []byte("after-b"), api.SourceChangeOpWrite)

	// Apply a.txt before injecting a panic on b.txt.
	mgr.sourceRewinds.Mutations = &rewindFaultJournal{delegate: mgr.sourceMutations, before: func(entryIndex int) {
		if entryIndex == 1 {
			panic("boom: injected rewind apply panic")
		}
	}}

	operationID := uuid.NewString()
	_, err = rewindTest(t, mgr, ctx, operationID, sessionID, anchor.ID)
	if err == nil {
		t.Fatal("RewindToPrompt returned no error; want the panic surfaced as a rewind failure, not swallowed")
	}
	if !strings.Contains(err.Error(), "injected rewind apply panic") {
		t.Fatalf("RewindToPrompt err = %v, want it to name the panic", err)
	}

	// Rollback restores the applied path and preserves the untouched path.
	gotA, err := os.ReadFile(pathA)
	testutil.FailErr(t, "read a", err)
	if string(gotA) != "after-a" {
		t.Fatalf("a.txt = %q, want rolled back to after-a (pre-rewind-attempt state)", gotA)
	}
	gotB, err := os.ReadFile(pathB)
	testutil.FailErr(t, "read b", err)
	if string(gotB) != "after-b" {
		t.Fatalf("b.txt = %q, want untouched after-b", gotB)
	}

	msgs, err := mgr.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	if _, ok := findMessage(msgs, anchor.ID); !ok {
		t.Fatal("a rolled-back rewind must leave the transcript intact")
	}

	op, err := mgr.store.GetRewindOperation(ctx, operationID)
	testutil.FailErr(t, "get rewind operation", err)
	if op == nil || op.Status != "rolled_back" {
		t.Fatalf("rewind operation status = %+v, want rolled_back", op)
	}

	// Retry in the same process after rollback.
	mgr.sourceRewinds.Mutations = mgr.sourceMutations
	result, err := rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor.ID)
	testutil.FailErr(t, "rewind after recovery", err)
	if result == nil {
		t.Fatal("rewind after recovery returned a nil result")
	}
}

// Startup reclaims anchors left behind after the rewind commit.
func TestSweepCommittedRewindsReclaimsOrphanedAnchor(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := checkpointCaller(t, mgr)

	anchor := api.Message{
		ID: "u-sweep", Role: api.MessageRoleUser, Content: "change file",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
	}
	testutil.FailErr(t, "append", mgr.store.AppendMessages(ctx, sessionID, anchor))

	path := filepath.Join(dir, "file.txt")
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte("before"), 0o640))
	cpStore := sessioncheckpoint.New(mgr.dataDir, dir, mgr.store)
	_, err := cpStore.Open(t.Context(), sessionID, anchor.ID)
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture", cpStore.CapturePreImage(t.Context(), sessionID, anchor.ID, "file.txt"))
	testutil.FailErr(t, "turn writes", os.WriteFile(path, []byte("after"), 0o640))
	recordRewindTestEffect(t, mgr, sessionID, "file.txt", []byte("before"), []byte("after"), api.SourceChangeOpWrite)

	rootID := RootSessionID(ctx, mgr.store, sessionID)
	// The existence check below fails loudly if this checkpoint layout changes.
	anchorDir := filepath.Join(enginepaths.ProjectCheckpointDir(enginepaths.SessionCheckpointsRootUnder(mgr.dataDir), filepath.Clean(dir)), "anchors", rootID, anchor.ID)
	if _, err := os.Stat(anchorDir); err != nil {
		t.Fatalf("anchor dir missing before rewind: %v", err)
	}

	msgs, err := mgr.store.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	anchorIDs := eligibleAnchorIDsFrom(msgs, anchor.ID)
	operationID := uuid.NewString()
	result := &RewindResult{}
	// Stop after commit to leave anchor cleanup pending.
	_, err = mgr.executeRewind(ctx, cpStore, rootID, operationID, rewindInputDigest(sessionID, anchor.ID), sessionID, anchor.ID, anchorIDs, rewindTestDigest(t, mgr, sessionID, anchor.ID), result)
	testutil.FailErr(t, "execute rewind", err)

	if _, err := os.Stat(anchorDir); err != nil {
		t.Fatalf("anchor dir missing right after commit (test setup invalid): %v", err)
	}

	testutil.FailErr(t, "sweep committed rewinds", mgr.sweepCommittedRewinds(ctx))

	if _, err := os.Stat(anchorDir); !os.IsNotExist(err) {
		t.Fatalf("anchor dir still present after sweep, err=%v", err)
	}

	// Retained receipts remain available for retries.
	op, err := mgr.store.GetRewindOperation(ctx, operationID)
	testutil.FailErr(t, "get rewind operation", err)
	if op == nil || op.Status != "committed" {
		t.Fatalf("operation = %+v, want still committed (within retention)", op)
	}
}

type rewindFaultJournal struct {
	delegate sourceeffect.Journal
	before   func(int)
	count    int
}

func (j *rewindFaultJournal) PrepareEffect(ctx context.Context, p sourceeffect.Plan) (sourceeffect.Pending, error) {
	if p.Record.Cause == "session_rewind" {
		n := j.count
		j.count++
		j.before(n)
	}
	return j.delegate.PrepareEffect(ctx, p)
}

func (j *rewindFaultJournal) RemoveEntry(ctx context.Context, r sourceeffect.Removal) (string, error) {
	return j.delegate.RemoveEntry(ctx, r)
}

func rewindTestDigest(t *testing.T, m *Manager, id, anchor string) string {
	t.Helper()
	preview, err := m.PreviewRewind(checkpointCaller(t, m), id, anchor)
	testutil.FailErr(t, "preview rewind", err)
	if len(preview.Issues) > 0 {
		t.Fatalf("preview issues: %+v", preview.Issues)
	}
	return preview.PlanDigest
}
