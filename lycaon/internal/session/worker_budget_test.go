package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMaybeWorkerBudgetExhaustedHint(t *testing.T) {
	msgs := []api.Message{{
		Role: api.MessageRoleUser,
		Kind: api.MessageKindIterationCapCloseout,
	}}
	task := &api.WorkerTask{
		ID:             "job-1",
		ChildSessionID: "child-1",
		MaxToolLoops:   40,
		ToolLoopsUsed:  39,
	}
	code, data := maybeWorkerBudgetExhaustedHint("partial", task, msgs, spawn.DefaultWorkerToolBudget())
	if code != WorkerBudgetExhaustedCode {
		t.Fatalf("code = %q want %s", code, WorkerBudgetExhaustedCode)
	}
	if data["child_session_id"] != "child-1" || data["suggested_resume_max"] != 80 {
		t.Fatalf("data = %v", data)
	}
}

// A resume carries the worker's own unanswered ask when it made one; otherwise
// it doubles the spent ceiling, always within the host maximum.
func TestWorkerResumeCeiling(t *testing.T) {
	budget := spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120}
	for _, tc := range []struct {
		name string
		task *api.WorkerTask
		want int
	}{
		{"doubles the spent ceiling", &api.WorkerTask{MaxToolLoops: 20}, 40},
		{"stops at the host maximum", &api.WorkerTask{MaxToolLoops: 90}, 120},
		{"honors the unanswered request", &api.WorkerTask{MaxToolLoops: 20, BudgetRequest: &api.WorkerBudgetRequest{RequestedMax: 28}}, 28},
		{"defaults an unset ceiling", &api.WorkerTask{}, 40},
	} {
		if got := WorkerResumeCeiling(tc.task, budget); got != tc.want {
			t.Errorf("%s: WorkerResumeCeiling = %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestWorkerBudgetFactsMarkOnlyPartialFinishesAtTheCeiling(t *testing.T) {
	budget := spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120}
	partial := &api.WorkerResult{Status: string(api.WorkerSummaryStatusPartial)}
	exhausted := workerBudgetFacts(&api.WorkerTask{ID: "job-1", MaxToolLoops: 12, ToolLoopsUsed: 12, Result: partial}, budget)
	if !exhausted.Exhausted || exhausted.ResumeMax != 24 {
		t.Fatalf("partial at the ceiling = %+v want exhausted with resume 24", exhausted)
	}
	short := workerBudgetFacts(&api.WorkerTask{ID: "job-1", MaxToolLoops: 12, ToolLoopsUsed: 7, Result: partial}, budget)
	if short.Exhausted {
		t.Fatalf("a partial below the ceiling is not a budget exhaustion: %+v", short)
	}
	complete := workerBudgetFacts(&api.WorkerTask{ID: "job-1", MaxToolLoops: 12, ToolLoopsUsed: 12, Result: &api.WorkerResult{Status: "complete"}}, budget)
	if complete.Exhausted {
		t.Fatalf("a complete finish is not exhausted: %+v", complete)
	}
}

func TestMaybeWorkerBudgetExhaustedHintSkipsWithoutCloseout(t *testing.T) {
	task := &api.WorkerTask{ID: "job-1", MaxToolLoops: 40}
	code, _ := maybeWorkerBudgetExhaustedHint("partial", task, nil, spawn.DefaultWorkerToolBudget())
	if code != "" {
		t.Fatalf("code = %q want empty without iteration_cap closeout", code)
	}
}
