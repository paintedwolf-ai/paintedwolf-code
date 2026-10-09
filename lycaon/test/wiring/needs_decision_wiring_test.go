package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestNeedsDecisionRoundTrip(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)

	task := wire.WorkerTask{
		ParentSessionID: sess.ID,
		AgentType:       "implementer",
		Prompt:          "Wire the parser",
		Brief:           "fixture",
		Status:          wire.WorkerStatusPending,
		SpawnReason:     wire.SpawnReasonHumanRequest,
	}
	testutil.FailErr(t, "enqueue defaults", worker.ApplyEnqueueDefaults(&task, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	jobID, err := h.WorkerQueue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue", err)
	child, err := h.Store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "Wire the parser"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "set child", h.WorkerQueue.SetChildSessionID(ctx, jobID, child.ID))
	claimed, err := h.WorkerQueue.ClaimNext(ctx, worker.ClaimRequest{
		ProjectID:       sess.ProjectID,
		ExecutionTarget: wire.ExecutionTargetLocal,
		ClaimedBy:       "needs-decision-test",
	})
	testutil.FailErr(t, "claim worker", err)

	if _, err := h.ToolRegistry.Run(ctx, "request_decision", map[string]any{
		"question": "Use the existing parser or write a new one?",
		"options":  []any{"reuse internal/parse", "write a new parser"},
	}, wiringToolContext(child.ID, dir, jobID)); err != nil {
		testutil.FailErr(t, "request_decision", err)
	}

	status, err := h.SessionMgr.AppendWorkerSummary(ctx, sess.ID, session.WorkerSummaryInput{
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Status:         "complete",
	})
	testutil.FailErr(t, "append worker summary", err)
	if status != "needs_decision" {
		t.Fatalf("status = %q want needs_decision", status)
	}
	completed, err := h.WorkerQueue.Complete(ctx, claimed, wire.WorkerResult{Status: status})
	testutil.FailErr(t, "complete worker", err)
	if !completed {
		t.Fatal("worker completion claim lost")
	}
	if !wire.WorkerResultStatusReactable(status) {
		t.Fatal("needs_decision must wake the coordinator (reactable)")
	}
	parentMsgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "parent messages", err)
	if !anyMessageContains(parentMsgs, "decision_request_json") {
		t.Fatal("parent envelope missing structured decision")
	}
	var envelope string
	for _, m := range parentMsgs {
		if m.WorkerSummary != nil && strings.Contains(m.WorkerSummary.Envelope, "decision_request_json") {
			envelope = m.WorkerSummary.Envelope
			break
		}
	}
	env, ok := session.ParseWorkerCompletionEnvelope(envelope)
	if !ok || env.DecisionRequest == nil {
		t.Fatalf("structured decision missing: ok=%v", ok)
	}
	if env.DecisionRequest.Question != "Use the existing parser or write a new one?" {
		t.Fatalf("decision question = %q", env.DecisionRequest.Question)
	}
	if env.DecisionRequest.BlockerClass != wire.WorkerBlockerDecision {
		t.Fatalf("blocker_class = %q", env.DecisionRequest.BlockerClass)
	}

	out, err := h.ToolRegistry.Run(ctx, "answer_decision", map[string]any{
		"job_id": jobID, "option": "2",
	}, wiringToolContext(sess.ID, dir))
	testutil.FailErr(t, "answer_decision", err)
	if !strings.Contains(out, "resumed") {
		t.Fatalf("answer_decision out = %q", out)
	}

	childMsgs, err := h.Store.GetMessages(ctx, child.ID)
	testutil.FailErr(t, "child messages", err)
	if !anyMessageContains(childMsgs, "write a new parser") {
		t.Fatalf("child did not receive the decision")
	}
	if _, ok, err := h.SessionMgr.Decisions().Get(ctx, child.ID); err != nil || ok {
		t.Fatal("decision stash not cleared after answer")
	}

	pending, err := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, wire.WorkerStatusPending)
	testutil.FailErr(t, "list pending", err)
	resume := false
	for _, p := range pending {
		if p.ChildSessionID == child.ID && p.ID == jobID {
			resume = true
		}
	}
	if !resume {
		t.Fatalf("worker %s not requeued on child %s: %+v", jobID, child.ID, pending)
	}
}

func anyMessageContains(msgs []wire.Message, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, sub) || (m.WorkerSummary != nil && strings.Contains(m.WorkerSummary.Envelope, sub)) {
			return true
		}
	}
	return false
}

func wiringToolContext(sessionID, dir string, workerJobID ...string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID},
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID: "r1"},
	}
	if len(workerJobID) > 0 {
		tctx.Identity.WorkerJobID = workerJobID[0]
		tctx.Identity.ParentSessionID = "parent"
	}
	return tctx
}
