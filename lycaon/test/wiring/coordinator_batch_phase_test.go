package wiring

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorBatchPhaseAdvancesOnTaskDispatch(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "implementer patch",
			ToolCalls: []llm.MockToolCall{{
				ID:   "t1",
				Name: "task",
				Args: TaskToolArgs("implementer", "Add a comment to main.go"),
			}},
			FollowUpText: "Dispatched implementer.",
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

	if _, err := h.SessionMgr.Prompt(ctx, sess.ID, "dispatch implementer patch"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	var phase string
	testutil.WaitFor(t, 5*time.Second, func() bool {
		state := h.SessionMgr.BuildImplementSessionState(ctx, sess)
		phase = state.BatchPhase
		return phase == batch.PhaseDispatch
	})
	if phase != batch.PhaseDispatch {
		t.Fatalf("batch phase = %q want dispatch after task()", phase)
	}
}

func TestCoordinatorBatchPhaseSynthesizeAfterWorkerDrain(t *testing.T) {
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

	if _, err := h.SessionMgr.Prompt(ctx, sess.ID, "Add a TODO comment via implementer"); err != nil {
		testutil.FailErr(t, "Prompt dispatch", err)
	}
	if err := DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID); err != nil {
		testutil.FailErr(t, "DrainPendingWorkerJobs", err)
	}

	// Worker completion wakes integration asynchronously.
	state := h.SessionMgr.BuildImplementSessionState(ctx, sess)
	if !testutil.WaitForNoFatal(5*time.Second, func() bool {
		state = h.SessionMgr.BuildImplementSessionState(ctx, sess)
		return state.BatchPhase == batch.PhaseIntegrate
	}) {
		t.Fatalf("implement state = %+v, want integrate after write worker drain with pending overlay", state)
	}
}
