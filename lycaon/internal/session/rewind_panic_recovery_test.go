package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcerewind"
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
	testutil.FailErr(t, "append", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(ctx, sessionID, anchor))

	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	testutil.FailErr(t, "seed a", os.WriteFile(pathA, []byte("before-a"), 0o640))
	testutil.FailErr(t, "seed b", os.WriteFile(pathB, []byte("before-b"), 0o640))

	cpStore := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	_, err := cpStore.Open(t.Context(), sessionID, anchor.ID)
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture a", cpStore.CapturePreImage(t.Context(), sessionID, anchor.ID, "a.txt"))
	testutil.FailErr(t, "capture b", cpStore.CapturePreImage(t.Context(), sessionID, anchor.ID, "b.txt"))
	testutil.FailErr(t, "turn writes a", os.WriteFile(pathA, []byte("after-a"), 0o640))
	testutil.FailErr(t, "turn writes b", os.WriteFile(pathB, []byte("after-b"), 0o640))
	recordRewindTestEffect(t, mgr, sessionID, "a.txt", []byte("before-a"), []byte("after-a"), api.SourceChangeOpWrite)
	recordRewindTestEffect(t, mgr, sessionID, "b.txt", []byte("before-b"), []byte("after-b"), api.SourceChangeOpWrite)

	// Apply a.txt before injecting a panic on b.txt.
	originalSource := mgr.Chats.Rewinds.SourceRewinds()
	faultSource := &sourcerewind.Service{Planner: originalSource.Planner, Mutations: &rewindFaultJournal{delegate: originalSource.Mutations, before: func(entryIndex int) {
		if entryIndex == 1 {
			panic("boom: injected rewind apply panic")
		}
	}}}
	mgr.Chats.Rewinds.SetSourceRewinds(faultSource)

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

	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	if _, ok := rewindFixtureHasMessage(msgs, anchor.ID); !ok {
		t.Fatal("a rolled-back rewind must leave the transcript intact")
	}

	op, err := mgr.Coordinator.Context.Sessions.(Store).GetRewindOperation(ctx, operationID)
	testutil.FailErr(t, "get rewind operation", err)
	if op == nil || op.Status != "rolled_back" {
		t.Fatalf("rewind operation status = %+v, want rolled_back", op)
	}

	// Retry in the same process after rollback.
	mgr.Chats.Rewinds.SetSourceRewinds(originalSource)
	result, err := rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor.ID)
	testutil.FailErr(t, "rewind after recovery", err)
	if result == nil {
		t.Fatal("rewind after recovery returned a nil result")
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

func rewindTestDigest(t *testing.T, m *Host, id, anchor string) string {
	t.Helper()
	preview, err := m.Chats.Rewinds.PreviewRewind(checkpointCaller(t, m), id, anchor)
	testutil.FailErr(t, "preview rewind", err)
	if len(preview.Issues) > 0 {
		t.Fatalf("preview issues: %+v", preview.Issues)
	}
	return preview.PlanDigest
}
