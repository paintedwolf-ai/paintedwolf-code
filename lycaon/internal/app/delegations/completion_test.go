package delegations

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompletionCoverageValidationUsesDurableWorkerIdentity(t *testing.T) {
	database := testdbfixture.Open(t, "completion.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	runtime := New(database, nil, worker.WorkersConfig{})
	id, err := runtime.Queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{ParentSessionID: "parent", AgentType: "implementer", Prompt: "Inspect source", Brief: "Inspect source", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "enqueue completion worker", err)
	rejection := errors.New("coverage review not grounded")
	calls := 0
	validate := func(ctx context.Context, task *api.WorkerTask, review *api.CoverageReview) error {
		if ctx != t.Context() || task.ID != id || task.ParentSessionID != "parent" || review == nil {
			t.Fatalf("coverage validation lost durable worker context: %+v, %+v", task, review)
		}
		calls++
		return rejection
	}
	args := map[string]any{"leg_status": "complete", "coverage_review": map[string]any{}}
	invocation := tools.ToolContext{Identity: tools.InvocationIdentity{WorkerJobID: id}}
	record, err := runtime.DecodeCompleteLeg(t.Context(), args, invocation, validate)
	if record.LegStatus != "complete" || !errors.Is(err, rejection) || calls != 1 {
		t.Fatalf("coverage refusal discarded: %+v, %v, calls=%d", record, err, calls)
	}
	invocation.Identity.WorkerJobID = "missing-worker"
	if _, err := runtime.DecodeCompleteLeg(t.Context(), args, invocation, validate); err == nil || calls != 1 {
		t.Fatalf("missing worker bypassed durable validation: %v, calls=%d", err, calls)
	}
	// An invocation without worker identity has no durable job whose coverage
	// can be validated; it must not borrow the previous worker's review context.
	invocation.Identity.WorkerJobID = ""
	record, err = runtime.DecodeCompleteLeg(t.Context(), args, invocation, validate)
	if err != nil || record.LegStatus != "complete" || calls != 1 {
		t.Fatalf("non-worker completion borrowed worker review context: %+v,%v,calls=%d", record, err, calls)
	}
	invocation.Identity.WorkerJobID = id
	if _, err := runtime.DecodeCompleteLeg(t.Context(), map[string]any{}, invocation, validate); err == nil || calls != 1 {
		t.Fatalf("invalid completion reached coverage validator: %v, calls=%d", err, calls)
	}
	record, err = runtime.DecodeCompleteLeg(t.Context(), args, invocation, nil)
	if err != nil || record.LegStatus != "complete" {
		t.Fatalf("valid completion without additional review failed: %+v, %v", record, err)
	}
	jobs, err := runtime.BranchRetentionDeps().Jobs(t.Context())
	testutil.FailErr(t, "load durable branch retention jobs", err)
	if _, ok := jobs[id]; !ok || jobs[id].Sealed {
		t.Fatalf("pending completion lost unsealed branch retention: %+v", jobs)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.BranchRetentionDeps().Jobs(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("branch retention ignored query cancellation: %v", err)
	}
}
