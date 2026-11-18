package worker

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/session/decisions/decisionstest"
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
	decisions sessiondecisions.Store
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

// shownOnce lets each job's question be answered once, as a request that
// showed it would; hidden jobs were never shown.
type shownOnce struct {
	taken  map[string]bool
	hidden map[string]bool
}

func (s *shownOnce) Take(_, jobID string, _ api.WorkerDecisionRequest) bool {
	if s.hidden[jobID] || s.taken[jobID] {
		return false
	}
	s.taken[jobID] = true
	return true
}

func answerDecisionService(t *testing.T, q *answerStubQueue, decisions sessiondecisions.Store) (*AnswerDecisionService, *answerStubResolver) {
	t.Helper()
	resolver := &answerStubResolver{decisions: decisions}
	return &AnswerDecisionService{
		Queue:     q,
		Decisions: decisions,
		Resolver:  resolver,
		Shown:     &shownOnce{taken: map[string]bool{}, hidden: map[string]bool{}},
	}, resolver
}

// An answer to a question its request never showed, or a second answer to
// one it did, is refused before the worker is touched.
func TestAnswerDecisionNeedsAShownQuestion(t *testing.T) {
	q := &answerStubQueue{task: &api.WorkerTask{ID: "job-1", ParentSessionID: "parent-1", ChildSessionID: "child-1"}}
	decisions := decisionstest.New(t, decisionstest.Job{ID: "job-1", Child: "child-1"})
	testutil.FailErr(t, "store decision", decisions.Put(context.Background(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "q", BlockerClass: api.WorkerBlockerDecision,
	}))
	svc, resolver := answerDecisionService(t, q, decisions)
	svc.Shown.(*shownOnce).hidden["job-1"] = true
	_, err := svc.AnswerJob(context.Background(), "parent-1", "fixer", "job-1", "yes", "")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != DecisionNotShownCode || len(resolver.appended) != 0 {
		t.Fatalf("unshown answer = %v appended = %d", err, len(resolver.appended))
	}
	delete(svc.Shown.(*shownOnce).hidden, "job-1")
	svc.Shown.(*shownOnce).taken["job-1"] = true
	if _, err := svc.AnswerJob(context.Background(), "parent-1", "fixer", "job-1", "yes", ""); !errors.As(err, &reject) || reject.Code != DecisionNotShownCode {
		t.Fatalf("second answer = %v", err)
	}
}

func TestAnswerDecisionResumesWorker(t *testing.T) {
	q := &answerStubQueue{task: &api.WorkerTask{
		ID: "job-1", ParentSessionID: "parent-1", ChildSessionID: "child-1",
		AgentType: "implementer", ProjectID: testdbseed.DefaultProjectID,
		Prompt: "Refactor the project", Brief: "Refactor the project",
	}}
	decisions := decisionstest.New(t, decisionstest.Job{ID: "job-1", Child: "child-1"})
	testutil.FailErr(t, "store decision", decisions.Put(context.Background(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Refactor or work around?", Options: []string{"do A", "do B"}, BlockerClass: api.WorkerBlockerDecision,
	}))
	svc, resolver := answerDecisionService(t, q, decisions)
	out, err := svc.AnswerJob(context.Background(), "parent-1", "fixer", "job-1", "2", "")
	testutil.FailErr(t, "answer", err)
	if out.Option != "do B" || resolver.sessionID != "child-1" || len(resolver.appended) != 1 || !strings.Contains(resolver.appended[0].Content, "do B") {
		t.Fatalf("answer = %+v appended = %+v", out, resolver.appended)
	}
	if _, ok, err := decisions.Get(context.Background(), "child-1"); err != nil || ok {
		t.Fatalf("decision after answer: ok=%v err=%v", ok, err)
	}
	if _, err := svc.AnswerJob(context.Background(), "other", "fixer", "job-1", "do A", ""); err == nil {
		t.Fatal("answered a job from another session")
	}
}

// A decision that offers options takes one by text or number, or any other
// answer as written; one that names another job is no pending decision of
// that worker.
func TestAnswerDecisionValidation(t *testing.T) {
	q := &answerStubQueue{task: &api.WorkerTask{ID: "job-1", ParentSessionID: "parent-1", ChildSessionID: "child-1"}}
	decisions := decisionstest.New(t, decisionstest.Job{ID: "job-other", Child: "child-1"}, decisionstest.Job{ID: "job-1", Child: "child-1"})
	testutil.FailErr(t, "store decision", decisions.Put(context.Background(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-other", Question: "q", Options: []string{"a", "b"}, BlockerClass: api.WorkerBlockerDecision,
	}))
	svc, _ := answerDecisionService(t, q, decisions)
	_, err := svc.AnswerJob(context.Background(), "parent-1", "fixer", "job-1", "a", "")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != DecisionNotFoundCode || reject.Data["worker"] != "fixer" {
		t.Fatalf("mismatched decision: %v", err)
	}
	testutil.FailErr(t, "store matching decision", decisions.Put(context.Background(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "q", Options: []string{"a", "b"}, BlockerClass: api.WorkerBlockerDecision,
	}))
	out, err := svc.AnswerJob(context.Background(), "parent-1", "fixer", "job-1", "neither: keep both paths", "")
	testutil.FailErr(t, "free-text answer", err)
	if out.Option != "neither: keep both paths" {
		t.Fatalf("free-text answer = %+v", out)
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
	svc, _ := answerDecisionService(t, &answerStubQueue{}, decisionstest.New(t))
	_, err := svc.AnswerJob(t.Context(), "parent", "fixer", "missing", "a", "coordinator")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != DecisionNotFoundCode || reject.Data["worker"] != "fixer" {
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

// An open question has no options, so the coordinator's words are the answer,
// including one that looks like an option number.
func TestAnswerDecisionOpenQuestionTakesFreeText(t *testing.T) {
	q := &answerStubQueue{task: &api.WorkerTask{ID: "job-1", ParentSessionID: "parent-1", ChildSessionID: "child-1"}}
	decisions := decisionstest.New(t, decisionstest.Job{ID: "job-1", Child: "child-1"})
	testutil.FailErr(t, "store decision", decisions.Put(context.Background(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Which encoding does the caller expect?", BlockerClass: api.WorkerBlockerDecision,
	}))
	svc, resolver := answerDecisionService(t, q, decisions)
	out, err := svc.AnswerJob(context.Background(), "parent-1", "fixer", "job-1", "2", "")
	testutil.FailErr(t, "answer", err)
	if out.Option != "2" || len(resolver.appended) != 1 || !strings.HasPrefix(resolver.appended[0].Content, DecisionAnswerCoordinator+": 2\n") {
		t.Fatalf("answer = %+v appended = %+v", out, resolver.appended)
	}
}
