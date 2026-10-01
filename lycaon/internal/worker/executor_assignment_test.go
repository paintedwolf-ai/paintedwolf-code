package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type assignmentHistoryRunner struct {
	fakePromptRunner
	messages []api.Message
	err      error
	childID  string
	jobID    string
}

func (r *assignmentHistoryRunner) GetWorkerJobMessages(ctx context.Context, childID, jobID string) ([]api.Message, error) {
	r.childID, r.jobID = childID, jobID
	if r.err != nil {
		return nil, r.err
	}
	reports, err := r.fakePromptRunner.GetWorkerJobMessages(ctx, childID, jobID)
	return append(append([]api.Message(nil), r.messages...), reports...), err
}

func TestExecuteRetryKeepsExistingAssignment(t *testing.T) {
	for _, attempt := range []int{1, 2} {
		runner := &assignmentHistoryRunner{messages: []api.Message{
			{ID: "assignment", Role: api.MessageRoleUser, WorkerID: "job-1", Content: "original rendered assignment"},
			{ID: "tool-result", Role: api.MessageRoleTool, WorkerID: "job-1", Content: "done"},
		}}
		exec := newTestWorkerExecutor(t, runner)
		_, err := exec.Execute(t.Context(), api.WorkerTask{
			ID: "job-1", ParentSessionID: "parent-1", ChildSessionID: "child-1",
			Prompt: "assignment rendered differently on retry", Brief: "fixture",
			AgentType: "implementer", Attempt: attempt,
		}, WorkerRunContext{ProjectDir: "/p"})
		testutil.FailErr(t, "execute worker retry", err)
		if runner.childID != "child-1" || runner.jobID != "job-1" {
			t.Fatalf("assignment history scope = %q/%q", runner.childID, runner.jobID)
		}
		if len(runner.spawnCalls) != 0 || len(runner.promptCalls) != 1 || runner.promptCalls[0].text != "" {
			t.Fatalf("attempt %d replayed assignment: spawns=%v prompts=%+v", attempt, runner.spawnCalls, runner.promptCalls)
		}
	}
}

func TestExecuteAssignmentHistoryFailureStopsPrompt(t *testing.T) {
	failure := errors.New("transcript unavailable")
	runner := &assignmentHistoryRunner{err: failure}
	exec := newTestWorkerExecutor(t, runner)
	_, err := exec.Execute(t.Context(), api.WorkerTask{
		ID: "job-1", ParentSessionID: "parent-1", ChildSessionID: "child-1",
		Prompt: "assignment", Brief: "fixture", AgentType: "implementer",
	}, WorkerRunContext{ProjectDir: "/p"})
	if !errors.Is(err, failure) || len(runner.promptCalls) != 0 {
		t.Fatalf("history failure: err=%v prompts=%+v", err, runner.promptCalls)
	}
}
