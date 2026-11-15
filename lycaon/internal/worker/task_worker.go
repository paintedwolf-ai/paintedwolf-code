package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskWorkerNameTakenCode refuses a task whose name another worker of the
// session already holds; that worker is directed with instruct_worker.
const TaskWorkerNameTakenCode = "TASK_WORKER_NAME_TAKEN"

func replayTaskCall(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, sourceDigest string) (string, bool, error) {
	if deps.TaskReceipt == nil || strings.TrimSpace(tctx.Identity.ToolCallID) == "" {
		return "", false, nil
	}
	prior, err := deps.TaskReceipt(ctx, tctx.Identity.SessionID, strings.TrimSpace(tctx.Identity.ToolCallID))
	if err != nil || prior == nil {
		return "", false, err
	}
	if err := compareTaskReceipt(prior, api.WorkerTask{SourceToolCallID: tctx.Identity.ToolCallID, SourceArgsDigest: sourceDigest}); err != nil {
		return "", true, err
	}
	scope := project.ScopeFromToolContext(tctx.Identity.ProjectID, tctx.Source.ActiveRootID, tctx.Source.Roots, tctx.ActiveRootPath())
	name, err := startWorkerRound(ctx, deps, tctx, *prior, nil, nil)
	if err != nil {
		return "", true, err
	}
	if err := accountReplayedReview(ctx, deps, tctx, prior.ID); err != nil {
		return "", true, err
	}
	if err := deps.Queue.ReleaseAdmitted(ctx, prior.ID); err != nil {
		return "", true, err
	}
	out, err := taskEnqueueOutput(tctx, scope, *prior, name)
	return out, true, err
}

func parseTaskFilesArg(args map[string]any) []string {
	var files []string
	if raw, ok := args["files"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				files = append(files, strings.TrimSpace(s))
			}
		}
	}
	return files
}

func applyPlannedWorkerTask(
	ctx context.Context,
	deps TaskToolDeps,
	tctx tools.ToolContext,
	projectID string,
	workID string,
	name string,
	scope *api.TaskScope,
	files []string,
	after []string,
) (*workerrecords.Worker, []string, []string, error) {
	if deps.Plan == nil || strings.TrimSpace(workID) == "" {
		return nil, after, files, nil
	}
	var mode string
	var paths []string
	if scope != nil {
		mode = string(scope.Mode)
		paths = scope.Paths
	}
	w, mergedAfter, ok, err := deps.Plan.PreparePlannedTask(ctx, coordinatorActor(tctx), projectID, workID, mode, paths, after)
	if err != nil || !ok {
		return nil, after, files, err
	}
	if name != "" && name != w.Name {
		return nil, nil, nil, &toolrejection.ToolReject{
			Code: "TOOL_ARGS_INVALID",
			Data: map[string]any{"tool": "task", "field": "name", "worker": w.Name, "reason": "planned_worker_name_mismatch"},
		}
	}
	if len(files) == 0 && len(w.Files) > 0 {
		files = append([]string(nil), w.Files...)
	}
	if scope != nil && len(scope.Paths) == 0 && len(w.Files) > 0 {
		scope.Paths = append([]string(nil), w.Files...)
	}
	return &w, mergedAfter, files, nil
}

func ensureTaskWorker(
	ctx context.Context,
	deps TaskToolDeps,
	tctx tools.ToolContext,
	task api.WorkerTask,
	name string,
	brief string,
	planned *workerrecords.Worker,
) (workerrecords.WorkerReceipt, bool, error) {
	if planned != nil {
		if brief != "" && brief != planned.Brief {
			if _, err := deps.WorkerRecords.SetBrief(ctx, coordinatorActor(tctx), taskWorkerOp(tctx, "brief"), planned.ID, brief); err != nil {
				return workerrecords.WorkerReceipt{}, false, err
			}
		}
		return workerrecords.WorkerReceipt{WorkerID: planned.ID, Name: planned.Name}, false, nil
	}
	if deps.Plan != nil && task.WorkflowRunID != "" && task.WorkflowWorkID != "" {
		if err := deps.Plan.SupersedeSettledWorkflowWorker(ctx, coordinatorActor(tctx), taskWorkerOp(tctx, "supersede"), task.WorkflowRunID, task.WorkflowPhase, task.WorkflowWorkID); err != nil {
			return workerrecords.WorkerReceipt{}, false, err
		}
	}
	created, err := createTaskWorker(ctx, deps, tctx, task, name, brief)
	return created, err == nil, err
}

func workerNameArg(args map[string]any) (string, error) {
	raw, ok := args["name"]
	if !ok || raw == nil {
		return "", nil
	}
	name, ok := raw.(string)
	name = strings.TrimSpace(name)
	if !ok || name == "" || len([]rune(name)) > 64 {
		return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "task", "field": "name", "reason": "invalid_worker_name"}}
	}
	return name, nil
}

func taskWorkerOp(tctx tools.ToolContext, part string) workerrecords.Op {
	return workerrecords.Op{ID: "task:" + strings.TrimSpace(tctx.Identity.SessionID) + ":" + strings.TrimSpace(tctx.Identity.ToolCallID) + ":" + part}
}

// createTaskWorker records the worker a task starts. Without a name the host
// derives one from the agent.
func createTaskWorker(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, task api.WorkerTask, name, brief string) (workerrecords.WorkerReceipt, error) {
	if strings.TrimSpace(tctx.Identity.ToolCallID) == "" {
		return workerrecords.WorkerReceipt{}, fmt.Errorf("task requires a tool call identity")
	}
	mode := workerrecords.ScopeRead
	if task.EffectiveScope().IsWrite() {
		mode = workerrecords.ScopeWrite
	}
	receipt, err := deps.WorkerRecords.CreateWorker(ctx, coordinatorActor(tctx), taskWorkerOp(tctx, "worker"), workerrecords.WorkerSpec{
		Name: name, AgentType: task.AgentType, ScopeMode: mode, Brief: brief,
		WorkflowRunID: task.WorkflowRunID, WorkflowPhase: task.WorkflowPhase, WorkflowWorkID: task.WorkflowWorkID,
	})
	if errors.Is(err, workerrecords.ErrNameTaken) {
		return receipt, &toolrejection.ToolReject{Code: TaskWorkerNameTakenCode, Data: map[string]any{"tool": "task", "worker": name}}
	}
	return receipt, err
}

// startWorkerRound admits the task's job as its worker's first round, bound
// to subject for a reviewer, and returns the worker's name. A replayed task
// call finds the worker and round its first call created.
func startWorkerRound(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, task api.WorkerTask, created *workerrecords.WorkerReceipt, subject *workerrecords.ReviewSubject) (string, error) {
	actor := coordinatorActor(tctx)
	if round, admitted, err := deps.WorkerRecords.RoundByJob(ctx, task.ID); err != nil {
		return "", err
	} else if admitted {
		if created != nil && created.Name != "" {
			return created.Name, nil
		}
		w, wErr := deps.WorkerRecords.Worker(ctx, actor, round.WorkerID)
		if wErr != nil {
			return "", wErr
		}
		return w.Name, nil
	}
	var receipt workerrecords.WorkerReceipt
	if created != nil && created.WorkerID != "" {
		receipt = *created
	} else {
		var err error
		receipt, err = resolveReplayedWorkerReceipt(ctx, deps, actor, tctx, task)
		if err != nil {
			return "", err
		}
	}
	target := string(task.ExecutionTarget)
	if target == "" {
		target = string(api.ExecutionTargetLocal)
	}
	spec := workerrecords.RoundSpec{JobID: task.ID, ExecutionTarget: target}
	if subject != nil {
		var err error
		if spec.ReviewSubject, err = json.Marshal(subject); err != nil {
			return "", err
		}
	}
	if _, err := deps.WorkerRecords.AdmitRound(ctx, actor, taskWorkerOp(tctx, "round"), receipt.WorkerID, spec); err != nil {
		return "", err
	}
	return receipt.Name, nil
}

func resolveReplayedWorkerReceipt(ctx context.Context, deps TaskToolDeps, actor workerrecords.Actor, tctx tools.ToolContext, task api.WorkerTask) (workerrecords.WorkerReceipt, error) {
	kind, raw, ok, err := deps.WorkerRecords.Operation(ctx, actor, taskWorkerOp(tctx, "worker").ID)
	if err != nil {
		return workerrecords.WorkerReceipt{}, err
	}
	if ok && kind == "create_worker" {
		var w workerrecords.WorkerReceipt
		err = json.Unmarshal(raw, &w)
		return w, err
	}
	if strings.TrimSpace(task.WorkflowWorkID) != "" {
		workers, wErr := deps.WorkerRecords.Workers(ctx, actor)
		if wErr != nil {
			return workerrecords.WorkerReceipt{}, wErr
		}
		for _, w := range workers {
			if w.ClosedAt == "" && w.WorkflowWorkID == task.WorkflowWorkID {
				return workerrecords.WorkerReceipt{WorkerID: w.ID, Name: w.Name}, nil
			}
		}
	}
	return workerrecords.WorkerReceipt{}, fmt.Errorf("task %s started no worker", task.ID)
}

// bindReviewSubject validates review_of and resolves the sealed result it addresses.
func bindReviewSubject(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, args map[string]any, workflowWorkID string, after []string, scope api.TaskScope) (*workerrecords.ReviewSubject, error) {
	raw, given := args["review_of"]
	if !given || raw == nil {
		return nil, nil
	}
	handle, _ := raw.(string)
	handle = strings.TrimSpace(handle)
	invalid := func(reason string) error {
		return &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "task", "field": "review_of", "reason": reason}}
	}
	switch {
	case handle == "":
		return nil, invalid("result_handle_required")
	case scope.IsWrite():
		return nil, invalid("review_requires_read_scope")
	case strings.TrimSpace(scope.BaseOverlayID) != "":
		return nil, invalid("review_excludes_base_overlay_id")
	case len(after) > 0:
		return nil, invalid("review_excludes_after_workers")
	case workflowWorkID != "":
		return nil, invalid("review_excludes_workflow_work_id")
	}
	subject, err := deps.Reviews.Review(ctx, coordinatorActor(tctx), handle)
	if err != nil {
		return nil, reviewReject(handle, err)
	}
	return &subject, nil
}

// resolveTaskAddresses turns the worker names in after_workers into the jobs
// of those workers' latest rounds, and a result handle in base_overlay_id
// into its job. A prerequisite follows its producer's later rounds on its
// own, so the latest round is where it starts.
func resolveTaskAddresses(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, after []string, scope *api.TaskScope) ([]string, error) {
	actor := coordinatorActor(tctx)
	jobs := make([]string, 0, len(after))
	for _, name := range after {
		w, err := deps.WorkerRecords.Resolve(ctx, actor, name)
		if errors.Is(err, workerrecords.ErrWorkerNotFound) {
			return nil, &toolrejection.ToolReject{Code: "WORKER_NAME_UNKNOWN", Data: map[string]any{"tool": "task", "worker": name}}
		}
		if err != nil {
			return nil, err
		}
		rounds, err := deps.WorkerRecords.Rounds(ctx, actor, w.ID)
		if err != nil {
			return nil, err
		}
		if len(rounds) == 0 {
			return nil, &toolrejection.ToolReject{Code: "WORKER_NAME_UNKNOWN", Data: map[string]any{"tool": "task", "worker": name}}
		}
		jobs = append(jobs, rounds[len(rounds)-1].JobID)
	}
	if scope != nil && strings.TrimSpace(scope.BaseOverlayID) != "" {
		handle := strings.TrimSpace(scope.BaseOverlayID)
		ref, err := deps.WorkerRecords.Result(ctx, actor, handle)
		if errors.Is(err, workerrecords.ErrResultNotFound) {
			return nil, &toolrejection.ToolReject{Code: "RESULT_HANDLE_UNKNOWN", Data: map[string]any{"tool": "task", "overlay_id": handle}}
		}
		if err != nil {
			return nil, err
		}
		scope.BaseOverlayID = ref.JobID
	}
	return jobs, nil
}

// accountReplayedReview finishes a review whose first call admitted the round
// and stopped before accounting for the subject.
func accountReplayedReview(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, jobID string) error {
	round, ok, err := deps.WorkerRecords.RoundByJob(ctx, jobID)
	if err != nil || !ok {
		return err
	}
	subject, reviewer := round.Subject()
	if !reviewer {
		return nil
	}
	return deps.Reviews.ReviewStarted(ctx, coordinatorActor(tctx), taskWorkerOp(tctx, "review"), subject)
}

// ReviewOfNotAdmittedCode refuses a review of a result whose changes were
// already landed or rejected.
const ReviewOfNotAdmittedCode = "REVIEW_OF_NOT_ADMITTED"

// ReviewOfNoChangesCode refuses a review of a result that changed nothing.
const ReviewOfNoChangesCode = "REVIEW_OF_NO_CHANGES"

func reviewReject(handle string, err error) error {
	var settled *workerops.ReviewSettledError
	switch {
	case errors.Is(err, workerrecords.ErrResultNotFound):
		return &toolrejection.ToolReject{Code: "RESULT_HANDLE_UNKNOWN", Data: map[string]any{"tool": "task", "overlay_id": handle}}
	case errors.Is(err, workerops.ErrReviewNoChanges):
		return &toolrejection.ToolReject{Code: ReviewOfNoChangesCode, Data: map[string]any{"tool": "task", "overlay_id": handle}}
	case errors.As(err, &settled):
		status := api.WorkerMergeStatusRejected
		if settled.Change == workerrecords.ChangeLanded {
			status = api.WorkerMergeStatusMerged
		}
		return &toolrejection.ToolReject{Code: ReviewOfNotAdmittedCode, Data: map[string]any{"tool": "task", "overlay_id": handle, "worker": settled.Worker, "merge_status": string(status)}}
	}
	return err
}
