package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestExtendWorkerBudgetWiring(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)

	task := wire.WorkerTask{
		ParentSessionID: sess.ID,
		AgentType:       "implementer",
		Prompt:          "long leg",
		Brief:           "fixture",
		Status:          wire.WorkerStatusRunning,
		MaxToolLoops:    40,
		ToolLoopsUsed:   30,
	}
	testutil.FailErr(t, "enqueue defaults", worker.ApplyEnqueueDefaults(&task, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	jobID, err := h.WorkerQueue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue", err)
	child, err := h.Store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "long leg"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "set child", h.WorkerQueue.SetChildSessionID(ctx, jobID, child.ID))

	out, err := h.ToolRegistry.Run(ctx, "extend_worker_budget", map[string]any{
		"job_id":         jobID,
		"max_tool_loops": 80,
	}, wiringToolContext(sess.ID, dir))
	testutil.FailErr(t, "extend_worker_budget", err)
	if !strings.Contains(out, `"new_max":80`) {
		t.Fatalf("extend out = %q", out)
	}
	updated, ok := h.WorkerQueue.Get(jobID)
	if !ok || updated.MaxToolLoops != 80 {
		t.Fatalf("queue max = %d want 80", updated.MaxToolLoops)
	}
	reloaded, err := h.Store.Get(ctx, child.ID)
	testutil.FailErr(t, "reload child", err)
	if reloaded.MaxToolLoops != 80 {
		t.Fatalf("child max = %d want 80", reloaded.MaxToolLoops)
	}
}

func TestDeclineWorkerBudgetWiring(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	task := wire.WorkerTask{
		ParentSessionID: sess.ID,
		AgentType:       "security-reviewer",
		Prompt:          "trace the entry points",
		Brief:           "fixture",
		Status:          wire.WorkerStatusRunning,
		MaxToolLoops:    20,
		ToolLoopsUsed:   14,
	}
	testutil.FailErr(t, "enqueue defaults", worker.ApplyEnqueueDefaults(&task, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	jobID, err := h.WorkerQueue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue", err)
	child, err := h.Store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "security-reviewer", Prompt: "trace the entry points"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "set child", h.WorkerQueue.SetChildSessionID(ctx, jobID, child.ID))

	childCtx := wiringToolContext(child.ID, dir, jobID)
	childCtx.Identity.ParentSessionID = sess.ID
	_, err = h.ToolRegistry.Run(ctx, worker.RequestBudgetTool, map[string]any{
		"rounds": 6, "remaining_work": []any{"trace the alternate callers"},
	}, childCtx)
	testutil.FailErr(t, "request_budget", err)
	if asked, ok := h.WorkerQueue.Get(jobID); !ok || asked.BudgetRequest == nil {
		t.Fatalf("job after request = %+v, want an open request", asked)
	}

	_, err = h.ToolRegistry.Run(ctx, "decline_worker_budget", map[string]any{"job_id": jobID}, wiringToolContext(sess.ID, dir))
	testutil.FailErr(t, "decline_worker_budget", err)
	declined, ok := h.WorkerQueue.Get(jobID)
	if !ok || declined.BudgetRequest != nil || declined.MaxToolLoops != 20 {
		t.Fatalf("job after decline = %+v, want the request closed at a ceiling of 20", declined)
	}
}

func TestWorkerBudgetExhaustedEnvelopeWiring(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)

	task := wire.WorkerTask{
		ID:              "job-exhaust",
		ParentSessionID: sess.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType:     "implementer",
		Prompt:        "work",
		Brief:         "fixture",
		Status:        wire.WorkerStatusRunning,
		MaxToolLoops:  40,
		ToolLoopsUsed: 39,
	}
	_, err = h.WorkerQueue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue", err)
	child, err := h.Store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "work"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "set child", h.WorkerQueue.SetChildSessionID(ctx, task.ID, child.ID))

	// The message kind records the iteration-cap closeout.
	closeout := "forced final turn — " + promptloop.TurnCloseoutReasonText(promptloop.TurnCloseoutIterationCap)
	testutil.FailErr(t, "child msgs", h.Store.AppendMessages(ctx, child.ID, wire.Message{
		Role:       wire.MessageRoleUser,
		Kind:       wire.MessageKindIterationCapCloseout,
		WorkerID:   task.ID,
		Visibility: wire.MessageVisibilityInternal,
		Content:    closeout,
	}))

	status, err := h.SessionMgr.Workers.Summaries.Append(ctx, sess.ID, workeroutcomes.SummaryInput{
		JobID:          task.ID,
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Status:         "partial",
		HintCode:       workercompletion.WorkerCompletionReportMissingCode,
	})
	testutil.FailErr(t, "append summary", err)
	if status != "partial" {
		t.Fatalf("status = %q want partial", status)
	}
	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "parent msgs", err)
	if !anyMessageContains(msgs, workeroutcomes.WorkerBudgetExhaustedCode) {
		t.Fatal("parent transcript missing WORKER_BUDGET_EXHAUSTED")
	}
}
