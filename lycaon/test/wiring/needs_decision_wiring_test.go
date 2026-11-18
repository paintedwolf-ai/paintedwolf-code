package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerrecords"
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
	jobID, err := h.Workers.Queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue", err)
	child, err := h.Store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "Wire the parser"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "set child", h.Workers.Queue.SetChildSessionID(ctx, jobID, child.ID))
	records := workerrecords.New(h.DB)
	coord := workerrecords.Actor{SessionID: sess.ID, Role: workerrecords.RoleCoordinator, PrincipalID: sess.ID}
	w, err := records.CreateWorker(ctx, coord, workerrecords.Op{ID: "create-parser"}, workerrecords.WorkerSpec{
		Name: "parser", AgentType: "implementer", ScopeMode: "write", Brief: "fixture", ChildSessionID: child.ID,
	})
	testutil.FailErr(t, "create worker", err)
	_, err = records.AdmitRound(ctx, coord, workerrecords.Op{ID: "admit-parser"}, w.WorkerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit round", err)
	claimed, err := h.Workers.Queue.ClaimNext(ctx, worker.ClaimRequest{
		ProjectID:       sess.ProjectID,
		ExecutionTarget: wire.ExecutionTargetLocal,
		ClaimedBy:       "needs-decision-test",
	})
	testutil.FailErr(t, "claim worker", err)

	if _, err := h.ToolRegistry.Run(ctx, "request_decision", map[string]any{
		"question": "Use the existing parser or write a new one?",
		"options":  []any{"reuse internal/parse", "write a new parser"},
	}, WiringToolContext(child.ID, dir, jobID)); err != nil {
		testutil.FailErr(t, "request_decision", err)
	}

	status, err := h.Sessions.Manager.Workers.Summaries.Append(ctx, sess.ID, workeroutcomes.SummaryInput{
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Status:         "complete",
	})
	testutil.FailErr(t, "append worker summary", err)
	if status != "needs_decision" {
		t.Fatalf("status = %q want needs_decision", status)
	}
	completed, err := h.Workers.Queue.Complete(ctx, claimed, wire.WorkerResult{Status: status})
	testutil.FailErr(t, "complete worker", err)
	if !completed {
		t.Fatal("worker completion claim lost")
	}
	if !wire.WorkerResultStatusReactable(status) {
		t.Fatal("needs_decision must wake the coordinator (reactable)")
	}
	parentMsgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "parent messages", err)
	if !AnyMessageContains(parentMsgs, "decision_request_json") {
		t.Fatal("parent envelope missing structured decision")
	}
	var envelope string
	for _, m := range parentMsgs {
		if m.WorkerSummary != nil && strings.Contains(m.WorkerSummary.Envelope, "decision_request_json") {
			envelope = m.WorkerSummary.Envelope
			break
		}
	}
	env, ok := workercompletion.ParseWorkerCompletionEnvelope(envelope)
	if !ok || env.DecisionRequest == nil {
		t.Fatalf("structured decision missing: ok=%v", ok)
	}
	if env.DecisionRequest.Question != "Use the existing parser or write a new one?" {
		t.Fatalf("decision question = %q", env.DecisionRequest.Question)
	}
	if env.DecisionRequest.BlockerClass != wire.WorkerBlockerDecision {
		t.Fatalf("blocker_class = %q", env.DecisionRequest.BlockerClass)
	}

	// The coordinator's next request shows the question on its roster.
	_, _, err = h.Sessions.Manager.Coordinator.Assembly.WorkerRoster(ctx, sess.ID)
	testutil.FailErr(t, "render the roster", err)
	out, err := h.ToolRegistry.Run(ctx, "answer_decision", map[string]any{
		"worker": "parser", "answer": "2",
	}, WiringToolContext(sess.ID, dir))
	testutil.FailErr(t, "answer_decision", err)
	if !strings.Contains(out, "resumed") {
		t.Fatalf("answer_decision out = %q", out)
	}

	childMsgs, err := h.Store.GetMessages(ctx, child.ID)
	testutil.FailErr(t, "child messages", err)
	if !AnyMessageContains(childMsgs, "write a new parser") {
		t.Fatalf("child did not receive the decision")
	}
	if _, ok, err := h.Sessions.Manager.Decisions.Get(ctx, child.ID); err != nil || ok {
		t.Fatal("decision stash not cleared after answer")
	}

	pending, err := h.Workers.Queue.ListBySession(ctx, sess.ProjectID, sess.ID, wire.WorkerStatusPending)
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
