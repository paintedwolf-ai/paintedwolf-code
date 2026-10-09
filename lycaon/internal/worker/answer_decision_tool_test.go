package worker

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type answerStubQueue struct {
	WorkerQueue
	task *api.WorkerTask
}

func (q *answerStubQueue) Get(id string) (*api.WorkerTask, bool) {
	if q.task != nil && q.task.ID == id {
		return q.task, true
	}
	return nil, false
}

type answerStubResolver struct {
	decisions session.DecisionStore
	sessionID string
	appended  []api.Message
}

func (r *answerStubResolver) Resolve(ctx context.Context, decision api.WorkerDecisionRequest, msg api.Message) error {
	r.sessionID = decision.ChildSessionID
	r.appended = append(r.appended, msg)
	if err := r.decisions.Clear(ctx, decision.ChildSessionID); err != nil {
		return err
	}
	return nil
}

func answerDecisionService(t *testing.T, q *answerStubQueue, decisions session.DecisionStore) (*AnswerDecisionService, *answerStubResolver) {
	t.Helper()
	resolver := &answerStubResolver{decisions: decisions}
	return &AnswerDecisionService{
		Queue:     q,
		Decisions: decisions,
		Resolver:  resolver,
	}, resolver
}

func TestAnswerDecisionResumesWorker(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	q := &answerStubQueue{task: &api.WorkerTask{
		ID: "job-1", ParentSessionID: "parent-1", ChildSessionID: "child-1",
		AgentType: "implementer", ProjectID: testdbseed.DefaultProjectID,
		Prompt: "Refactor the project", Brief: "Refactor the project",
	}}
	decisions := session.NewMemoryDecisionStore()
	testutil.FailErr(t, "store decision", decisions.Put(context.Background(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Refactor or work around?", Options: []string{"do A", "do B"}, BlockerClass: api.WorkerBlockerDecision,
	}))
	svc, resolver := answerDecisionService(t, q, decisions)
	testutil.FailErr(t, "register", RegisterAnswerDecisionTool(reg, AnswerDecisionToolDeps{Answer: svc}))

	capture := &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), "answer_decision",
		map[string]any{"job_id": "job-1", "option": "2"},
		tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "parent-1"},
			Effects:  tools.InvocationEffects{Out: capture},
		})
	testutil.FailErr(t, "run", err)
	if capture.DisplaySubject != "Refactor the project · do B" {
		t.Fatalf("decision subject = %q", capture.DisplaySubject)
	}
	if !strings.Contains(out, "resumed") {
		t.Fatalf("out = %q", out)
	}
	if resolver.sessionID != "child-1" || len(resolver.appended) != 1 || !strings.Contains(resolver.appended[0].Content, "do B") {
		t.Fatalf("appended = %+v", resolver.appended)
	}
	if _, ok, err := decisions.Get(context.Background(), "child-1"); err != nil || ok {
		t.Fatalf("decision after answer: ok=%v err=%v", ok, err)
	}
}

func TestAnswerDecisionValidation(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	q := &answerStubQueue{task: &api.WorkerTask{ID: "job-1", ParentSessionID: "parent-1", ChildSessionID: "child-1"}}
	decisions := session.NewMemoryDecisionStore()
	testutil.FailErr(t, "store decision", decisions.Put(context.Background(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "q", Options: []string{"a", "b"}, BlockerClass: api.WorkerBlockerDecision,
	}))
	svc, _ := answerDecisionService(t, q, decisions)
	testutil.FailErr(t, "register", RegisterAnswerDecisionTool(reg, AnswerDecisionToolDeps{Answer: svc}))
	if _, err := reg.Run(context.Background(), "answer_decision",
		map[string]any{"job_id": "job-1", "option": "a"}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "other"},
		}); err == nil || !strings.Contains(err.Error(), DecisionSessionMismatchCode) {
		t.Fatalf("wrong-session err = %v", err)
	}
	if _, err := reg.Run(context.Background(), "answer_decision",
		map[string]any{"job_id": "job-1", "option": "zzz"}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "parent-1"},
		}); err == nil || !strings.Contains(err.Error(), "not one of") {
		t.Fatalf("bad-option err = %v", err)
	}
}

func TestAnswerDecisionJobIDMismatch(t *testing.T) {
	q := &answerStubQueue{task: &api.WorkerTask{
		ID: "job-wrong", ParentSessionID: "parent-1", ChildSessionID: "child-1",
	}}
	decisions := session.NewMemoryDecisionStore()
	testutil.FailErr(t, "store decision", decisions.Put(context.Background(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-expected", Question: "Pick one", Options: []string{"a", "b"}, BlockerClass: api.WorkerBlockerDecision,
	}))
	svc, _ := answerDecisionService(t, q, decisions)
	_, err := svc.AnswerJob(context.Background(), "parent-1", "job-wrong", "1", "")
	if err == nil || !strings.Contains(err.Error(), DecisionJobIDMismatchCode) {
		t.Fatalf("err = %v want %s", err, DecisionJobIDMismatchCode)
	}
}

func TestResolveDecisionOption(t *testing.T) {
	opts := []string{"do A", "do B"}
	if got, ok := resolveDecisionOption("2", opts); !ok || got != "do B" {
		t.Fatalf("index: %q %v", got, ok)
	}
	if got, ok := resolveDecisionOption("DO a", opts); !ok || got != "do A" {
		t.Fatalf("text: %q %v", got, ok)
	}
	if got, ok := resolveDecisionOption("3", []string{"3", "8"}); !ok || got != "3" {
		t.Fatalf("numeric option text must win over its ordinal: %q %v", got, ok)
	}
	if _, ok := resolveDecisionOption("9", opts); ok {
		t.Fatal("out-of-range index accepted")
	}
	if _, ok := resolveDecisionOption("nope", opts); ok {
		t.Fatal("unknown text accepted")
	}
}

func TestAnswerDecisionMissingJobRetainsTypedDecisionFacts(t *testing.T) {
	svc, _ := answerDecisionService(t, &answerStubQueue{}, session.NewMemoryDecisionStore())
	_, err := svc.AnswerJob(t.Context(), "parent", "missing", "a", "coordinator")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != DecisionNotFoundCode || reject.Data["job_id"] != "missing" {
		t.Fatalf("lost decision refusal: %#v / %v", reject, err)
	}
}

func TestWorkerSubjectRespectsSessionOwnership(t *testing.T) {
	q := &answerStubQueue{task: &api.WorkerTask{ID: "worker", ParentSessionID: "parent", Brief: "Repair login"}}
	for _, sessionID := range []string{"parent", "other"} {
		out := &tools.ToolInvocationOut{}
		captureWorkerSubject(tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sessionID},
			Effects:  tools.InvocationEffects{Out: out},
		}, q, "worker")
		expected := ""
		if sessionID == "parent" {
			expected = "Repair login"
		}
		if out.DisplaySubject != expected {
			t.Fatalf("%s subject = %q", sessionID, out.DisplaySubject)
		}
	}
}
