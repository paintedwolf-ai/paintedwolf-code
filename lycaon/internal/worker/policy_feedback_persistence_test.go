package worker

import (
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"reflect"
	"testing"
)

func TestWorkerPolicyFeedbackSurvivesQueueReopen(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	queue := NewSQLQueue(database, 1)
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, AgentType: "implementer", Prompt: "Inspect result", Brief: "Inspect result", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim worker", err)
	evaluation := workercompletion.WorkerSummaryEvalResult{HintCode: "REPORT_PROBE", HintEffect: oar.EffectBlock, HintCopy: map[string]string{"what": "Literal {{ value }}", "cause": "original observation", "why": "retain evidence", "fix": "inspect retained result", "instead": "report uncertainty"}, HintData: map[string]any{"observed": []any{"before"}}}
	feedback := evaluation.PolicyFeedback()
	evaluation.HintCopy["what"] = "changed copy"
	evaluation.HintData["observed"].([]any)[0] = "after"
	if feedback.Copy["what"] != "Literal {{ value }}" || feedback.Details["observed"].([]any)[0] != "before" {
		t.Fatal("feedback retained mutable evaluation data")
	}
	result := api.WorkerResult{Status: "partial", Summary: "Original brief", HintCode: feedback.Code, PolicyFeedback: feedback, Grounding: &api.CitationGrounding{Traced: false}}
	won, err := queue.Complete(t.Context(), claimed, result)
	testutil.FailErr(t, "commit worker result", err)
	if !won {
		t.Fatal("worker claim did not commit")
	}
	reopened := NewSQLQueue(database, 1)
	task, ok := reopened.Get(id)
	if !ok || task.Result == nil || !reflect.DeepEqual(task.Result.PolicyFeedback, feedback) || task.Result.Grounding == nil {
		t.Fatalf("frozen feedback lost on reopen: %+v", task)
	}
	pending, err := reopened.ListPendingOutcomes(t.Context())
	testutil.FailErr(t, "load pending projection", err)
	if len(pending) != 1 || !reflect.DeepEqual(pending[0].Result.PolicyFeedback, feedback) {
		t.Fatalf("pending delivery lost feedback: %+v", pending)
	}
}
