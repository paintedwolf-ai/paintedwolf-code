package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskToolArgs builds a bounded task charter and its write scope.
func TaskToolArgs(agentType, goal string, writePaths ...string) map[string]any {
	args := map[string]any{
		"agent_type": agentType,
		"brief": map[string]any{
			"goal": goal, "done_when": []any{"Return grounded results for the focused assignment."},
		},
	}
	if capable, ok := prompts.AgentMutationCapable(agentType); !ok || !capable {
		return args
	}
	paths := writePaths
	if len(paths) == 0 {
		paths = []string{"main.go"}
	}
	pathItems := make([]any, len(paths))
	for i, p := range paths {
		pathItems[i] = p
	}
	args["scope"] = map[string]any{
		"mode":  "write",
		"paths": pathItems,
	}
	return args
}

// MockCoordinatorCloseoutMarkdown builds a coordinator closeout citing each path.
func MockCoordinatorCloseoutMarkdown(synthesis string, citedPaths ...string) string {
	out := synthesis
	for _, path := range citedPaths {
		if path != "" {
			out += " [" + path + `:1 "x"]`
		}
	}
	return out
}

// MockWorkerCompletionJSON builds a worker finish-turn JSON report for wiring mocks.
// files_modified is host-authoritative (overlay diff), so a worker report never carries it.
func MockWorkerCompletionJSON(legStatus, brief string, objectives []string) string {
	remainingRisk := []string{}
	suggestedNextTask := ""
	if strings.TrimSpace(legStatus) != "complete" {
		remainingRisk = []string{"Worker reported incomplete work."}
		suggestedNextTask = "Dispatch a repair task."
	}
	payload := map[string]any{
		"leg_status":          legStatus,
		"objectives_met":      objectives,
		"remaining_risk":      remainingRisk,
		"suggested_next_task": suggestedNextTask,
		"brief":               brief,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// PromotePendingWriteOverlays merges completed write workers onto primary for wiring tests.
func PromotePendingWriteOverlays(ctx context.Context, h *Harness, projectID, parentSessionID string) error {
	if h == nil || h.Workers.Queue == nil || h.Sessions.Manager == nil || strings.TrimSpace(parentSessionID) == "" {
		return nil
	}
	jobs, err := h.Workers.Queue.ListBySession(ctx, projectID, parentSessionID, api.WorkerStatusComplete)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if !job.EffectiveScope().IsWrite() {
			continue
		}
		if job.MergeStatus == api.WorkerMergeStatusMerged ||
			job.MergeStatus == api.WorkerMergeStatusRejected ||
			job.MergeStatus == api.WorkerMergeStatusOrphaned ||
			job.MergeStatus == api.WorkerMergeStatusAborted {
			continue
		}
		if strings.TrimSpace(job.WorkspaceRoot) == "" {
			continue
		}
		if _, err := h.Sessions.Manager.ProjectControl.PromoteOverlay(ctx, parentSessionID, job.ID, api.PromoteOverlayInput{Detail: "hunks"}); err != nil {
			return err
		}
	}
	return nil
}

const workerDrainQuiescenceTimeout = 30 * time.Second

// DrainPendingWorkerJobs runs all pending local worker jobs and waits until the queue is quiescent.
// parentSessionID, when non-empty, also drains deferred coordinator loop after workers finish.
func DrainPendingWorkerJobs(ctx context.Context, h *Harness, projectID, parentSessionID string) error {
	if h == nil || h.Workers.Queue == nil || h.Sessions.Manager == nil {
		return errors.New("harness worker queue or session manager missing")
	}
	drainCtx, cancel := context.WithTimeout(ctx, testutil.Timeout(workerDrainQuiescenceTimeout))
	defer cancel()
	exec := wiringWorkerExecutor(h)
	bridge := &worker.SessionOutcomeBridge{Workers: h.Sessions.Manager.Coordinator.Workers, Loop: h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges, Results: h.Sessions.Manager.Workers.Results, State: h.Sessions.Manager.Workers.State, Closure: h.Sessions.Manager.Coordinator.ProgressClosure}
	for {
		if err := drainCtx.Err(); err != nil {
			return err
		}
		task, err := h.Workers.Queue.ClaimNext(drainCtx, worker.ClaimRequest{
			ClaimedBy:       "wiring-test",
			ExecutionTarget: api.ExecutionTargetLocal,
		})
		if errors.Is(err, worker.ErrNoPendingJobs) {
			if workerCycleQuiescent(drainCtx, h, projectID, parentSessionID) {
				if parentSessionID != "" {
					h.Sessions.Manager.Coordinator.Batch.Reconcile(drainCtx, parentSessionID)
					h.Sessions.Manager.Runner.Coordinator.CoordinatorLoop().Nudges.DrainPending(drainCtx, parentSessionID)
				}
				return nil
			}
			if drainCtx.Err() != nil {
				return drainCtx.Err()
			}
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
		if task == nil {
			if workerCycleQuiescent(drainCtx, h, projectID, parentSessionID) {
				if parentSessionID != "" {
					h.Sessions.Manager.Coordinator.Batch.Reconcile(drainCtx, parentSessionID)
					h.Sessions.Manager.Runner.Coordinator.CoordinatorLoop().Nudges.DrainPending(drainCtx, parentSessionID)
				}
				return nil
			}
			continue
		}
		result, err := executeWorkerWithinDrain(drainCtx, h, exec, *task)
		if err != nil {
			return err
		}
		won, err := h.Workers.Queue.Complete(drainCtx, task, result)
		if err != nil {
			return err
		}
		if !won {
			return worker.ErrClaimLost
		}
		if err := bridge.OnWorkerComplete(drainCtx, task.ID, result); err != nil {
			return err
		}
		if err := h.Workers.Queue.MarkOutcomeDelivered(drainCtx, task.ID); err != nil {
			return err
		}
		bridge.OnOutcomeDelivered(drainCtx, *task)
	}
}

type workerDrainResult struct {
	result api.WorkerResult
	err    error
}

func executeWorkerWithinDrain(
	ctx context.Context,
	h *Harness,
	exec *worker.LocalWorkerExecutor,
	task api.WorkerTask,
) (api.WorkerResult, error) {
	done := make(chan workerDrainResult, 1)
	go func() {
		result, err := exec.Execute(ctx, task, worker.RunContextForTask(task, task.WorkspacePath))
		done <- workerDrainResult{result: result, err: err}
	}()
	select {
	case out := <-done:
		return out.result, out.err
	case <-ctx.Done():
		// The harness deadline explicitly cancels checkpoint waits.
		lookupCtx, cancelLookup := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		current, ok := h.Workers.Queue.Lookup(lookupCtx, task.ID)
		cancelLookup()
		if ok && current != nil && current.ChildSessionID != "" {
			h.Sessions.Manager.Runner.Execution.Cancel(current.ChildSessionID)
		}
		if out := <-done; out.err != nil {
			return out.result, out.err
		}
		return api.WorkerResult{}, ctx.Err()
	}
}

func workerQueueQuiescent(ctx context.Context, h *Harness, projectID string) bool {
	running, err := h.Workers.Queue.List(ctx, projectID, api.WorkerStatusRunning)
	if err != nil || len(running) > 0 {
		return false
	}
	pending, err := h.Workers.Queue.List(ctx, projectID, api.WorkerStatusPending)
	if err != nil {
		return false
	}
	for _, job := range pending {
		if len(job.AfterWorkers) == 0 || h.Workers.Records == nil {
			return false
		}
		waiting, wErr := workerops.PrerequisitePending(ctx, h.Workers.Records.LatestRoundJob, h.Workers.Queue, job)
		if wErr != nil || !waiting {
			return false
		}
	}
	return true
}

func workerCycleQuiescent(
	ctx context.Context,
	h *Harness,
	projectID, parentSessionID string,
) bool {
	if !workerQueueQuiescent(ctx, h, projectID) {
		return false
	}
	if parentSessionID == "" {
		return true
	}
	idle, err := workeroutcomes.ParentSessionWorkerCycleIdle(
		ctx, h.Workers.Queue, projectID, parentSessionID, "",
	)
	return err == nil && idle
}

// FailOnEndedWorker fails t when a worker job in the project ended without completing, so a
// wait for an outcome that needs the job stops on the job's own failure.
func FailOnEndedWorker(t testing.TB, h *Harness, ctx context.Context, projectID string) {
	t.Helper()
	ended, err := h.Workers.Queue.List(ctx, projectID, api.WorkerStatusFailed, api.WorkerStatusCanceled)
	testutil.FailErr(t, "list ended worker jobs", err)
	for _, task := range ended {
		t.Fatalf("worker job %s (%s) ended %s: %s", task.ID, task.AgentType, task.Status, task.Error)
	}
}
