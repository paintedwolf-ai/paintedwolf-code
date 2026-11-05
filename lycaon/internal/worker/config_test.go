package worker

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoadWorkersConfig(t *testing.T) {
	cfg, err := LoadWorkersConfig()
	testutil.FailErr(t, "LoadWorkersConfig failed", err)
	if cfg.Poller.MaxConcurrency != spawn.MaxInFlightTaskWorkers {
		t.Fatalf("max_concurrency = %d want %d", cfg.Poller.MaxConcurrency, spawn.MaxInFlightTaskWorkers)
	}
	if cfg.Poller.MaintenanceIntervalMs != 5000 {
		t.Fatalf("maintenance_interval_ms = %d", cfg.Poller.MaintenanceIntervalMs)
	}
}

func TestWorkflowWorkIdentitySurvivesQueuePersistence(t *testing.T) {
	store := obligationTestStore(t)
	task := api.WorkerTask{ID: "job", ProjectID: testdbseed.DefaultProjectID, WorkflowRunID: "run-1", WorkflowPhase: "execute", WorkflowWorkID: "leg-1", AgentType: "security-reviewer", Prompt: "Review", Brief: "Review", Status: api.WorkerStatusPending, ExecutionTarget: api.ExecutionTargetLocal}
	testutil.FailErr(t, "insert bound task", store.InsertTask(t.Context(), task))
	rows, err := store.ListByWorkflowRunID(t.Context(), "run-1")
	testutil.FailErr(t, "read bound task", err)
	if len(rows) != 1 || rows[0].WorkflowPhase != "execute" || rows[0].WorkflowWorkID != "leg-1" {
		t.Fatalf("tasks=%#v", rows)
	}
}

func TestWorkerHonorsProviderEmptyCompletionRetryability(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		err := fmt.Errorf("worker turn: %w", &failure.ProviderEmptyCompletionError{Reason: "length", Terminal: true, Retryable: retryable})
		if got := workerAttemptRetryable(&api.WorkerTask{Attempt: 1}, err); got != retryable {
			t.Fatalf("retryable=%v: worker retry = %v", retryable, got)
		}
	}
	if workerAttemptRetryable(&api.WorkerTask{Attempt: 1}, failure.ErrProviderOutputTruncated) {
		t.Fatal("worker retried native output truncation")
	}
	if workerAttemptRetryable(&api.WorkerTask{Attempt: 1}, &providerretry.ProviderRequestRejectedError{Status: 400}) {
		t.Fatal("worker retried request rejection")
	}
}

func TestSharedContextPreservesContractAndHasIndependentByteBudget(t *testing.T) {
	contract := "type Item = {\n  value: string;\n}\n\nrender(item);"
	args := map[string]any{"brief": map[string]any{"goal": "integrate", "done_when": []string{"shared interface works"}}, "shared_context": contract}
	charter, err := parseTaskCharter(args)
	testutil.FailErr(t, "parse shared contract", err)
	if charter.SharedContext != contract || !strings.Contains(formatTaskCharter(charter), contract) {
		t.Fatal("shared contract formatting changed")
	}
	args["shared_context"] = strings.Repeat("a", 8192)
	_, err = parseTaskCharter(args)
	testutil.FailErr(t, "accept independent context allowance", err)
	args["shared_context"] = strings.Repeat("界", 2731)
	if _, err := parseTaskCharter(args); err == nil {
		t.Fatal("accepted context exceeding byte budget")
	}
}

func TestPlannedCharterKeepsHostAssignment(t *testing.T) {
	planned := api.WorkerTaskCharter{Goal: "Inspect the full boundary", DoneWhen: []string{"Trace every entry point"}, SharedContext: "Local authenticated system"}
	got, err := plannedTaskCharter(&planned, map[string]any{"brief": map[string]any{"goal": "Skip the boundary", "done_when": []any{"Read one file"}}})
	testutil.FailErr(t, "bind planned charter", err)
	if got.Goal != planned.Goal || !reflect.DeepEqual(got.DoneWhen, planned.DoneWhen) || !strings.Contains(got.SharedContext, "Coordinator supplement") {
		t.Fatalf("planned assignment changed: %+v", got)
	}
	direct, err := plannedTaskCharter(&planned, nil)
	testutil.FailErr(t, "dispatch with work id only", err)
	if !reflect.DeepEqual(direct, planned) {
		t.Fatalf("unexpected charter: %+v", direct)
	}
}

func TestDraftScratchEnqueuePersistsWorkspaceRootID(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	rootID, scratch := testdbseed.InsertDraftScratchRoot(t, sqlDB, testdbseed.DefaultProjectID)

	task := api.WorkerTask{
		ID:              "job-draft-1",
		AgentType:       "implementer",
		Prompt:          "fixture",
		Brief:           "fixture",
		Scope:           &api.TaskScope{Mode: "write", Paths: []string{"src"}},
		WorkspaceRootID: rootID,
		WorkspacePath:   scratch,
		Status:          api.WorkerStatusPending,
		ExecutionTarget: api.ExecutionTargetLocal,
	}
	scope := project.ProjectScope{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspaceRootID: rootID,
		WorkspacePath:   scratch,
		HasRoots:        true,
	}
	if err := ApplyEnqueueDefaults(&task, scope, DefaultWorkersConfig()); err != nil {
		testutil.FailErr(t, "ApplyEnqueueDefaults", err)
	}
	if task.WorkspaceRootID != rootID {
		t.Fatalf("workspace_root_id = %q want %q", task.WorkspaceRootID, rootID)
	}
	if err := store.InsertTask(context.Background(), task); err != nil {
		testutil.FailErr(t, "InsertTask", err)
	}
}

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

// notifyDuringCheckQueue fires during the first active-job check.
type notifyDuringCheckQueue struct {
	WorkerQueue
	waiter          *ParentWorkerWaiter
	parentSessionID string
	calls           int
}

func (q *notifyDuringCheckQueue) ListBySession(_ context.Context, _, _ string, _ ...api.WorkerStatus) ([]api.WorkerTask, error) {
	q.calls++
	if q.calls == 1 {
		// Complete while the waiter is registered.
		q.waiter.NotifyParent(q.parentSessionID)
		return []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusRunning}}, nil
	}
	return nil, nil
}

func (*notifyDuringCheckQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}

func TestAwaitParentSessionWorkersDoesNotMissWakeupBetweenCheckAndRegister(t *testing.T) {
	waiter := NewParentWorkerWaiter()
	queue := &notifyDuringCheckQueue{waiter: waiter, parentSessionID: "parent-1"}
	lim := settings.DefaultSessionLimits()
	lim.AwaitParentWorkersTimeoutSec = 1

	err := AwaitParentSessionWorkers(t.Context(), queue, waiter, testdbseed.DefaultProjectID, "parent-1", lim)
	testutil.FailErr(t, "await parent session workers", err)

	if queue.calls != 2 {
		t.Fatalf("ListBySession calls = %d, want 2 (initial check + recheck after wake)", queue.calls)
	}
	if len(waiter.waiters) != 0 {
		t.Fatalf("waiters remaining = %d, want 0 after settlement", len(waiter.waiters))
	}
}
