package wiring

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func routingTurnHistory(history []api.Message, prompt string) []api.Message {
	out := append([]api.Message(nil), history...)
	msg := api.Message{
		Role:       api.MessageRoleUser,
		Origin:     api.MessageOriginUser,
		Visibility: api.MessageVisibilityTranscript,
		Content:    prompt,
	}
	if prompt == surface.HostLoopWakeSentinel {
		msg.Origin = api.MessageOriginHost
		msg.Visibility = api.MessageVisibilityInternal
		msg.Kind = api.MessageKindHostLoopWake
	}
	return append(out, msg)
}

func TestInvestigateTaskFanOutBlocksInvestigateUntilWorkersIdle(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "parallel modules",
			ToolCalls: []llm.MockToolCall{{
				ID:   "t1",
				Name: "task",
				Args: TaskToolArgs("implementer", "Add module scaffold under pkg/a", "pkg/a/**"),
			}},
			FollowUpText: "Dispatched implementer for module A.",
		},
	}})
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	_ = writeImplementFixture(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "build parallel modules with implementer"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	var state surface.ImplementSessionState
	testutil.WaitFor(t, 5*time.Second, func() bool {
		state = h.SessionMgr.Workers.State.ForSession(ctx, sess)
		if state.WorkersInFlight > 0 {
			return true
		}
		jobs, listErr := h.WorkerQueue.List(ctx, testdbseed.DefaultProjectID, api.WorkerStatusPending, api.WorkerStatusRunning)
		testutil.FailErr(t, "WorkerQueue.List", listErr)
		if len(jobs) > 0 {
			state.WorkersInFlight = len(jobs)
			return true
		}
		return false
	})
	if state.WorkersInFlight == 0 {
		t.Fatal("expected workers in flight after task() from investigate turn")
	}

	msgs, err := h.SessionMgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	wakeProfile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		sess,
		routingTurnHistory(msgs, surface.HostLoopWakeSentinel),
		state,
	)
	if wakeProfile.SurfaceID == tools.SurfaceImplementInvestigate {
		t.Fatalf("loop wake with workers in flight must not select investigate, got %q", wakeProfile.SurfaceID)
	}
	if wakeProfile.SurfaceID != surface.SurfaceImplementPark {
		t.Fatalf("loop wake surface = %q want %q", wakeProfile.SurfaceID, surface.SurfaceImplementPark)
	}
}

func TestInvestigateReturnsAfterWorkersCompleteAndNoQueuedPromotion(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "TODO comment",
			ToolCalls: []llm.MockToolCall{{
				ID:   "t1",
				Name: "task",
				Args: TaskToolArgs("implementer", "Add a // TODO: review here comment at the top of main.go"),
			}},
			FollowUpText: "Queued implementer.",
		},
		{
			Pattern: "Add a // TODO: review here",
			ToolCalls: []llm.MockToolCall{{
				ID:   "w1",
				Name: "write",
				Args: map[string]any{
					"path":    "main.go",
					"content": "// TODO: review here\npackage main\n\nfunc main() {}\n",
				},
			}},
			FollowUpText: "Added TODO comment.",
		},
		{
			Pattern:      "summarize what changed",
			FollowUpText: "Worker updated main.go with a TODO comment.",
		},
	}})
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	_ = writeImplementFixture(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "Add a TODO comment via implementer"); err != nil {
		testutil.FailErr(t, "Prompt dispatch", err)
	}
	if err := DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID); err != nil {
		testutil.FailErr(t, "DrainPendingWorkerJobs", err)
	}

	msgs, err := h.SessionMgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	state := h.SessionMgr.Workers.State.ForSession(ctx, sess)
	if len(state.PendingOverlayIDs) == 0 {
		t.Fatalf("expected pending overlay promote after write worker drain, PendingOverlayIDs=%v", state.PendingOverlayIDs)
	}
	if err := PromotePendingWriteOverlays(ctx, h, sess.ProjectID, sess.ID); err != nil {
		testutil.FailErr(t, "PromotePendingWriteOverlays", err)
	}
	msgs, err = h.SessionMgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after promote", err)
	state = h.SessionMgr.Workers.State.ForSession(ctx, sess)
	if len(state.PendingOverlayIDs) > 0 {
		t.Fatalf("expected no pending overlay promote after promote_overlay, PendingOverlayIDs=%v", state.PendingOverlayIDs)
	}

	// Wait for the batch ledger and follow-up surface to settle.
	var followProfile surface.TurnProfile
	settled := testutil.WaitForNoFatal(15*time.Second, func() bool {
		state = h.SessionMgr.Workers.State.ForSession(ctx, sess)
		if state.WorkersInFlight == 0 && state.BatchPhase != batch.PhaseDispatch {
			msgs, err = h.SessionMgr.Runner.Transcript.GetMessages(ctx, sess.ID)
			if err == nil && len(state.PendingOverlayIDs) == 0 {
				runCtx, rerr := h.SessionMgr.Coordinator.Context.RunContext(ctx, sess.ID)
				if rerr == nil {
					followProfile = surface.ResolveTurnProfile(runCtx, sess, routingTurnHistory(msgs, "summarize what changed"), state)
					if followProfile.SurfaceID == tools.SurfaceImplementInvestigate {
						return true
					}
				}
			}
		}
		return false
	})
	if !settled {
		runCtx, _ := h.SessionMgr.Coordinator.Context.RunContext(ctx, sess.ID)
		jobs, _ := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID)
		t.Fatalf("follow-up surface did not settle: profile=%q state=%+v run=%+v jobs=%+v", followProfile.SurfaceID, state, runCtx, jobs)
	}
	if followProfile.SurfaceID != tools.SurfaceImplementInvestigate {
		runCtx, _ := h.SessionMgr.Coordinator.Context.RunContext(ctx, sess.ID)
		jobs, _ := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID)
		t.Fatalf("visible user follow-up surface = %q want investigate; state=%+v run=%+v jobs=%+v", followProfile.SurfaceID, state, runCtx, jobs)
	}

	seqBefore := state.BatchSeq
	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "summarize what changed"); err != nil {
		testutil.FailErr(t, "Prompt follow-up", err)
	}
	state = h.SessionMgr.Workers.State.ForSession(ctx, sess)
	if state.BatchSeq <= seqBefore {
		t.Fatalf("batch_seq = %d want > %d after visible user follow-up (batch epoch reset)", state.BatchSeq, seqBefore)
	}
}
