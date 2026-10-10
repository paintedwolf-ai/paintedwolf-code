package wiring

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Rewind has to leave the session's live state agreeing with its transcript. A
// stale batch phase, a queued next-turn prompt, or a leftover kick would all act
// on a timeline the user just deleted — silently, and in the coordinator's
// favour rather than the user's.

func TestRewindResetsCoordinatorBatchAtTheNewBoundary(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: "first ask", FollowUpText: MockCoordinatorCloseoutJSON("Did the first ask.", "")},
		{Pattern: "second ask", FollowUpText: MockCoordinatorCloseoutJSON("Did the second ask.", "")},
	}})
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := h.OwnerCtx(t, context.Background())
	projectDir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, projectDir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, projectDir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "first ask"); err != nil {
		testutil.FailErr(t, "first prompt", err)
	}
	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "second ask"); err != nil {
		testutil.FailErr(t, "second prompt", err)
	}

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	anchors := visibleUserMessageIDs(t, msgs)
	if len(anchors) != 2 {
		t.Fatalf("visible user messages = %d, want 2", len(anchors))
	}

	seqBefore := implementBatchSeq(t, h, ctx, sess.ID)

	if _, err := h.Sessions.Manager.Chats.Rewinds.RewindToPrompt(ctx, uuid.NewString(), sess.ID, anchors[1], rewindDigest(t, h.Sessions.Manager, ctx, sess.ID, anchors[1])); err != nil {
		testutil.FailErr(t, "rewind to the second ask", err)
	}

	state := implementState(t, h, ctx, sess.ID)
	if state.BatchPhase != "pre_dispatch" {
		t.Fatalf("batch_phase = %q, want pre_dispatch at the new boundary", state.BatchPhase)
	}
	// The bumped sequence is what fences kicks queued under the old batch: a
	// stale-seq kick is dropped rather than rendered against a removed boundary.
	if state.BatchSeq <= seqBefore {
		t.Fatalf("batch_seq = %d, want > %d so stale kicks are fenced", state.BatchSeq, seqBefore)
	}
}

// Rewinding to the first ask must leave an empty transcript and a checkpoint
// store with nothing left to restore from — the suffix anchors are invalid.
func TestRewindToFirstAskDropsSuffixCheckpoints(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: "first ask", FollowUpText: MockCoordinatorCloseoutJSON("Did the first ask.", "")},
		{Pattern: "second ask", FollowUpText: MockCoordinatorCloseoutJSON("Did the second ask.", "")},
	}})
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := h.OwnerCtx(t, context.Background())
	projectDir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, projectDir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, projectDir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	for _, prompt := range []string{"first ask", "second ask"} {
		if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, prompt); err != nil {
			testutil.FailErr(t, "prompt "+prompt, err)
		}
	}
	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	anchors := visibleUserMessageIDs(t, msgs)

	if _, err := h.Sessions.Manager.Chats.Rewinds.RewindToPrompt(ctx, uuid.NewString(), sess.ID, anchors[0], rewindDigest(t, h.Sessions.Manager, ctx, sess.ID, anchors[0])); err != nil {
		testutil.FailErr(t, "rewind to the first ask", err)
	}

	after, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages after rewind", err)
	if len(visibleUserMessageIDs(t, after)) != 0 {
		t.Fatalf("visible asks remain after rewinding to the first one: %d", len(visibleUserMessageIDs(t, after)))
	}

	root := filepath.Join(projectDir, settingsoverlay.DirName(), "checkpoints", sess.ID)
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		testutil.FailErr(t, "read checkpoint dir", err)
	}
	for _, e := range entries {
		for _, anchorID := range anchors {
			if e.Name() == anchorID {
				t.Fatalf("checkpoint for rewound anchor %s survived — its ask no longer exists", anchorID)
			}
		}
	}
}

func implementState(t *testing.T, h *Harness, ctx context.Context, sessionID string) surface.ImplementSessionState {
	t.Helper()
	sess, err := h.Store.Get(ctx, sessionID)
	testutil.FailErr(t, "get session", err)
	return h.Sessions.Manager.Workers.State.ForSession(ctx, sess)
}

func implementBatchSeq(t *testing.T, h *Harness, ctx context.Context, sessionID string) int {
	t.Helper()
	return implementState(t, h, ctx, sessionID).BatchSeq
}

// A session that dispatched a worker, then rewound past the dispatch. The idle
// gate means no worker is running at rewind time, so what matters is what a
// finished dispatch left behind — touch-ledger rows and a transcript the
// coordinator reads pending-overlay state out of.
func TestRewindPastAWorkerDispatchLeavesNoGhostState(t *testing.T) {
	h := BuildForTest(t)
	ctx := h.OwnerCtx(t, context.Background())
	projectDir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, projectDir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, projectDir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "first ask")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mustMessages(t, h, ctx, sess.ID))[0]

	jobID, err := h.Delegations.Queue.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: sess.ID,
		ProjectID:       sess.ProjectID,
		WorkspacePath:   projectDir,
		AgentType:       "implementer",
		Prompt:          "do a leg",
		Brief:           "fixture",
		Status:          api.WorkerStatusComplete,
	})
	testutil.FailErr(t, "enqueue worker", err)
	touches := h.Sessions.Manager.Workers.Workspaces.Touches
	touches.RecordTouch(jobID, "src/leg.go")
	if len(h.Sessions.Manager.Workers.Workspaces.Touches.Paths(jobID)) == 0 {
		t.Fatal("touch-ledger setup failed")
	}

	if _, err := h.Sessions.Manager.Chats.Rewinds.RewindToPrompt(ctx, uuid.NewString(), sess.ID, anchor, rewindDigest(t, h.Sessions.Manager, ctx, sess.ID, anchor)); err != nil {
		testutil.FailErr(t, "rewind past the dispatch", err)
	}

	if paths := h.Sessions.Manager.Workers.Workspaces.Touches.Paths(jobID); len(paths) != 0 {
		t.Fatalf("touch ledger still holds %v for a job whose dispatch was rewound", paths)
	}
	// Pending-overlay state is derived from the transcript, so the truncate is
	// what clears it — assert the derived view, not a private field.
	msgs := mustMessages(t, h, ctx, sess.ID)
	if ids := surface.PendingOverlaySummaryIDs(msgs); len(ids) != 0 {
		t.Fatalf("overlays still pending after rewind: %v", ids)
	}
	state := implementState(t, h, ctx, sess.ID)
	if state.BatchPhase != "pre_dispatch" {
		t.Fatalf("batch_phase = %q, want pre_dispatch", state.BatchPhase)
	}
}

func mustMessages(t *testing.T, h *Harness, ctx context.Context, sessionID string) []api.Message {
	t.Helper()
	msgs, err := h.Store.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	return msgs
}

func rewindDigest(t *testing.T, mgr *session.Host, ctx context.Context, sessionID, anchor string) string {
	t.Helper()
	preview, err := mgr.Chats.Rewinds.PreviewRewind(ctx, sessionID, anchor)
	testutil.FailErr(t, "preview rewind", err)
	if len(preview.Issues) > 0 {
		t.Fatalf("rewind issues: %+v", preview.Issues)
	}
	return preview.PlanDigest
}
