package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestQuestionTaskAdmissionUsesDurableAttempts(t *testing.T) {
	now := time.Now().UTC()
	def := workflowdef.ReviewLoopDef{FollowupAttempts: 2, RequiredAgents: []string{"skeptic"}}
	vars := SetHostVar(nil, reviewQuestionPath("challenge"), `[{"id":"question/c6","claim_id":"c6","missing_fact":"Trace admission","obligations":["execute/leg-1"]}]`)
	investigation := api.WorkerTask{WorkflowPhase: "challenge", WorkflowWorkID: "question/c6", Status: api.WorkerStatusComplete, CompletedAt: &now}
	partial := api.WorkerTask{WorkflowPhase: "challenge", WorkflowWorkID: "question/c6/review", AgentType: "skeptic", Status: api.WorkerStatusComplete, CreatedAt: now, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "partial"}}}
	successful := partial
	successful.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}
	for _, tc := range []struct {
		name, work, agent, reason string
		tasks                     []api.WorkerTask
	}{
		{name: "first investigation", work: "question/c6"},
		{name: "failed provider preserves allowance", work: "question/c6", tasks: []api.WorkerTask{{WorkflowPhase: "challenge", WorkflowWorkID: "question/c6", Status: api.WorkerStatusFailed}}},
		{name: "active duplicate", work: "question/c6", reason: "review_question_already_active", tasks: []api.WorkerTask{{WorkflowPhase: "challenge", WorkflowWorkID: "question/c6", Status: api.WorkerStatusPending}}},
		{name: "bounded investigations", work: "question/c6", reason: "review_question_attempts_exhausted", tasks: []api.WorkerTask{investigation, investigation}},
		{name: "unknown question", work: "question/other", reason: "unknown_review_question"},
		{name: "review before investigation", work: "question/c6/review", agent: "skeptic", reason: "review_question_investigation_required"},
		{name: "fresh reviewer", work: "question/c6/review", agent: "skeptic", tasks: []api.WorkerTask{investigation}},
		{name: "partial reviewer can recover", work: "question/c6/review", agent: "skeptic", tasks: []api.WorkerTask{investigation, partial, partial}},
		{name: "completed review is reused", work: "question/c6/review", agent: "skeptic", reason: "review_question_already_reviewed", tasks: []api.WorkerTask{investigation, successful}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &RunManager{WorkerTasks: func(context.Context, string) ([]api.WorkerTask, error) { return tc.tasks, nil }}
			task := &api.WorkerTask{WorkflowWorkID: tc.work, AgentType: tc.agent}
			err := mgr.assertQuestionTask(t.Context(), &api.WorkflowRun{ID: "run", CurrentPhase: "challenge"}, def, vars, task)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("work rejected: %v", err)
				}
				return
			}
			rejection := toolrejection.AsToolReject(err)
			if rejection == nil || rejection.Data["reason"] != tc.reason {
				t.Fatalf("rejection = %v, want %s", err, tc.reason)
			}
		})
	}
}
