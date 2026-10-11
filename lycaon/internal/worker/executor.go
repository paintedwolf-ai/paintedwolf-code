package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

type WorkerRunContext struct {
	ProjectDir string
}

type WorkerExecutor interface {
	Execute(ctx context.Context, task api.WorkerTask, run WorkerRunContext) (api.WorkerResult, error)
	AbortWorkerRuntime(ctx context.Context, task api.WorkerTask) error
}

// AbortWorkerRuntime releases the child runtime, including parked processes.
func (e *LocalWorkerExecutor) AbortWorkerRuntime(ctx context.Context, task api.WorkerTask) error {
	if task.ChildSessionID == "" {
		return nil
	}
	if e.WorkerCancellations == nil {
		return fmt.Errorf("worker runtime stop is not configured")
	}
	return e.WorkerCancellations.StopRuntime(ctx, task.ChildSessionID)
}

type LocalWorkerExecutor struct {
	Sessions            WorkerExecutionSessions
	Prompts             WorkerPromptExecution
	Transcripts         workercloseout.WorkerSummaryResolver
	Cancellations       WorkerPromptCancellation
	Graceful            WorkerGracefulCloseout
	WorkerCancellations WorkerCancellationRuntime
	ChildSessions       WorkerChildSessionBinder
	Workspace           WorkerProjectRoots
	Injects             *prompts.InjectRenderer
	TouchPaths          session.WorkerPhaseTouchPathsSource
	Reports             ChangeReportDeps
	Waits               *awaitstore.Store
}

type WorkerExecutionSessions interface {
	SpawnChild(context.Context, string, api.SpawnChildRequest) (*api.Session, error)
	workercloseout.WorkerSummaryFinalizeBounds
	SessionByID(ctx context.Context, id string) (*api.Session, error)
	SetWorkerMaxToolLoops(ctx context.Context, childSessionID string, maxToolLoops int) error
}

type WorkerGracefulCloseout interface {
	Closeout(string) (string, string, bool)
	Finish(string)
}
type WorkerPromptCancellation interface{ Cancel(string) }

type WorkerPromptExecution interface {
	Prompt(context.Context, string, string) (*promptresult.Result, error)
	PromptWorker(context.Context, string, string, string) (*promptresult.Result, error)
	workercloseout.HostTurnRunner
	PromptWorkerResume(context.Context, string, string, string, awaitstore.Condition, func() error) (*promptresult.Result, error)
}

type WorkerProjectRoots interface {
	ProjectRoots(context.Context, string) []projectroot.RootRef
}

// WorkerChildSessionBinder records the child session associated with a worker job.
type WorkerChildSessionBinder interface {
	SetChildSessionID(ctx context.Context, jobID, childSessionID string) error
}

var workerExecLog = observability.LazyComponent("worker_executor")

func NewLocalWorkerExecutor(sessions WorkerExecutionSessions, childSessions WorkerChildSessionBinder, workspace WorkerProjectRoots, prompts WorkerPromptExecution, transcripts workercloseout.WorkerSummaryResolver, cancellation WorkerPromptCancellation, graceful WorkerGracefulCloseout, workerCancellations WorkerCancellationRuntime) *LocalWorkerExecutor {
	return &LocalWorkerExecutor{Sessions: sessions, Prompts: prompts, Transcripts: transcripts, Cancellations: cancellation, Graceful: graceful, WorkerCancellations: workerCancellations, ChildSessions: childSessions, Workspace: workspace}
}

func (e *LocalWorkerExecutor) SetPromptInjects(renderer *prompts.InjectRenderer) {
	if e != nil {
		e.Injects = renderer
	}
}

func (e *LocalWorkerExecutor) SetPhaseTouchPaths(src session.WorkerPhaseTouchPathsSource) {
	if e != nil {
		e.TouchPaths = src
	}
}

func (e *LocalWorkerExecutor) Execute(ctx context.Context, task api.WorkerTask, run WorkerRunContext) (api.WorkerResult, error) {
	if e.Sessions == nil {
		return api.WorkerResult{}, &PermanentExecutionError{Err: errors.New("session manager not configured")}
	}
	if e.ChildSessions == nil {
		return api.WorkerResult{}, &PermanentExecutionError{Err: errors.New("worker child-session binder not configured")}
	}
	if strings.TrimSpace(task.ID) == "" {
		return api.WorkerResult{}, &PermanentExecutionError{Err: errors.New("worker task missing id")}
	}
	parentID := strings.TrimSpace(task.ParentSessionID)
	if parentID == "" {
		return api.WorkerResult{}, &PermanentExecutionError{Err: errors.New("worker task missing parent_session_id")}
	}

	agentType := strings.TrimSpace(task.AgentType)
	if agentType == "" {
		return api.WorkerResult{}, &PermanentExecutionError{Err: errors.New("worker task missing agent_type")}
	}
	prompt := guidance.StripHostBlocks(task.Prompt)
	if prompt == "" {
		return api.WorkerResult{}, &PermanentExecutionError{Err: errors.New("worker task missing prompt")}
	}
	var touchPaths []string
	if e.TouchPaths != nil {
		touchPaths = e.TouchPaths.PhaseTouchPaths(ctx, parentID)
	}
	builtPrompt, err := BuildWorkerPrompt(ctx, e.Injects, parentID, run.ProjectDir, touchPaths, task.Scope, prompt)
	if err != nil {
		return api.WorkerResult{}, fmt.Errorf("worker task preamble: %w", err)
	}
	prompt = builtPrompt

	child, reused, err := e.resolveChildSession(ctx, task, parentID, agentType, prompt)
	if err != nil {
		return api.WorkerResult{}, err
	}
	if err := e.ChildSessions.SetChildSessionID(ctx, task.ID, child.ID); err != nil {
		return api.WorkerResult{}, fmt.Errorf("persist worker child session: %w", err)
	}
	stopCancelWatch := watchWorkerPromptCancel(ctx, e.Cancellations, child.ID)
	defer stopCancelWatch()

	var waitLeaseID string
	var condition awaitstore.Condition
	var resumedWait bool
	if e.Waits != nil {
		waitLeaseID, condition, resumedWait, err = e.Waits.PendingWorkerResume(ctx, task.ID)
	}
	if err != nil {
		return api.WorkerResult{}, err
	}
	if resumedWait {
		_, err = e.Prompts.PromptWorkerResume(ctx, child.ID, task.ID, waitLeaseID, condition, func() error {
			return e.Waits.MarkResumeDelivered(ctx, waitLeaseID)
		})
	} else {
		promptText := ""
		if reused {
			promptText, err = e.assignmentForReusedChild(ctx, child.ID, task.ID, prompt)
			if err != nil {
				return api.WorkerResult{}, err
			}
		}
		_, err = e.Prompts.PromptWorker(ctx, child.ID, task.ID, promptText)
	}
	if err != nil {
		return api.WorkerResult{}, err
	}
	if e.Waits != nil {
		if _, active, waitErr := e.Waits.ForSession(ctx, child.ID); waitErr != nil {
			return api.WorkerResult{}, waitErr
		} else if active {
			return api.WorkerResult{Status: "waiting", Response: "parked"}, nil
		}
	}

	finalizeOpts := e.workerFinalizeOpts(ctx, task, child, run.ProjectDir)
	if _, reason, pending := e.Graceful.Closeout(child.ID); pending {
		result, finishErr := e.completeGracefulStop(ctx, task, child, reason, finalizeOpts)
		e.Graceful.Finish(child.ID)
		if finishErr != nil {
			return api.WorkerResult{}, finishErr
		}
		return result, nil
	}

	outcome, err := workercloseout.FinalizeWorkerSummaryForChild(ctx, e.Transcripts, e.Prompts, child.ID, agentType, finalizeOpts)
	if err != nil {
		return api.WorkerResult{}, err
	}
	workerExecLog.Info("worker summary resolved",
		"job_id", task.ID,
		"agent_type", agentType,
		"status", outcome.Status,
		"provenance", outcome.Provenance,
		"hint_code", outcome.HintCode,
		"summary_len", len(strings.TrimSpace(outcome.Summary)),
	)
	return api.WorkerResult{
		Summary:          outcome.Summary,
		CompletionReport: workercompletion.ReportWire(outcome.Report),
		Response:         "completed",
		Status:           outcome.Status,
		HintCode:         outcome.HintCode,
		PolicyFeedback:   outcome.PolicyFeedback,
		Grounding:        outcome.Grounding,
		HostAssembled:    outcome.HostAssembled,
	}, nil
}

// Retries reuse the job's persisted assignment.
func (e *LocalWorkerExecutor) assignmentForReusedChild(ctx context.Context, childID, jobID, prompt string) (string, error) {
	messages, err := e.Transcripts.GetWorkerJobMessages(ctx, childID, jobID)
	if err != nil {
		return "", fmt.Errorf("read worker assignment history: %w", err)
	}
	for _, message := range messages {
		if message.Role == api.MessageRoleUser && message.Kind == "" {
			return "", nil
		}
	}
	return prompt, nil
}

func (e *LocalWorkerExecutor) workerFinalizeOpts(
	ctx context.Context,
	task api.WorkerTask,
	child *api.Session,
	projectDir string,
) workercloseout.WorkerSummaryFinalizeOpts {
	opts := e.Sessions.WorkerSummaryFinalizeOpts(ctx, child)
	opts.AgentType = strings.TrimSpace(task.AgentType)
	opts.WorkerJobID = strings.TrimSpace(task.ID)
	opts.ProjectDir = strings.TrimSpace(projectDir)
	opts.ActiveRootID = strings.TrimSpace(child.WorkspaceRootID)
	if strings.TrimSpace(task.ProjectID) != "" && e.Workspace != nil {
		opts.ProjectRoots = e.Workspace.ProjectRoots(ctx, task.ProjectID)
	}
	return opts
}

func watchWorkerPromptCancel(ctx context.Context, cancellation WorkerPromptCancellation, sessionID string) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		cancellation.Cancel(sessionID)
		close(done)
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

func (e *LocalWorkerExecutor) resolveChildSession(
	ctx context.Context,
	task api.WorkerTask,
	parentID, agentType, prompt string,
) (*api.Session, bool, error) {
	childID := strings.TrimSpace(task.ChildSessionID)
	if childID != "" {
		child, err := e.Sessions.SessionByID(ctx, childID)
		if err != nil {
			return nil, false, err
		}
		if child == nil {
			return nil, false, fmt.Errorf("child session %s not found", childID)
		}
		if strings.TrimSpace(child.ParentSessionID) != parentID {
			return nil, false, fmt.Errorf("child session %s does not belong to parent %s", childID, parentID)
		}
		if task.MaxToolLoops > 0 {
			if err := e.Sessions.SetWorkerMaxToolLoops(ctx, child.ID, task.MaxToolLoops); err != nil {
				return nil, false, fmt.Errorf("worker max_tool_loops: %w", err)
			}
			child.MaxToolLoops = task.MaxToolLoops
		}
		workerExecLog.Debug("worker reusing child session",
			"job_id", task.ID,
			"parent_session_id", parentID,
			"child_session_id", child.ID,
			"agent_type", agentType,
		)
		return child, true, nil
	}
	child, err := e.Sessions.SpawnChild(ctx, parentID, api.SpawnChildRequest{
		AgentType:    agentType,
		Prompt:       prompt,
		Files:        task.Files,
		MaxToolLoops: task.MaxToolLoops,
		WorkerJobID:  task.ID,
	})
	if err != nil {
		return nil, false, err
	}
	if child == nil {
		return nil, false, fmt.Errorf("spawn child returned no session")
	}
	workerExecLog.Debug("worker child session spawned",
		"job_id", task.ID,
		"parent_session_id", parentID,
		"child_session_id", child.ID,
		"agent_type", agentType,
	)
	return child, false, nil
}

// RunContextForTask builds local project binding from a claimed task.
func RunContextForTask(task api.WorkerTask, fallbackPath string) WorkerRunContext {
	dir := task.PrimaryRootPath()
	if dir == "" {
		dir = strings.TrimSpace(fallbackPath)
	}
	return WorkerRunContext{ProjectDir: dir}
}

func DefaultExecutionTarget(cfg WorkersConfig) api.ExecutionTarget {
	switch strings.TrimSpace(cfg.Executor.DefaultExecutionTarget) {
	case string(api.ExecutionTargetRunner):
		return api.ExecutionTargetRunner
	default:
		return api.ExecutionTargetLocal
	}
}

// ApplyEnqueueDefaults validates and fills queue-provided task fields.
func ApplyEnqueueDefaults(task *api.WorkerTask, scope project.ProjectScope, cfg WorkersConfig) error {
	if task == nil {
		return fmt.Errorf("task required")
	}
	if err := normalizeWorkerInstructions(task); err != nil {
		return err
	}
	if strings.TrimSpace(scope.ProjectID) == "" {
		return fmt.Errorf("project_id required")
	}
	if strings.TrimSpace(task.AgentType) == "" {
		task.AgentType = orchestration.ProfileImplementer
	}
	if strings.TrimSpace(task.ProjectID) == "" {
		task.ProjectID = scope.ProjectID
	}
	if strings.TrimSpace(task.WorkspaceRootID) == "" && strings.TrimSpace(scope.WorkspaceRootID) != "" {
		task.WorkspaceRootID = scope.WorkspaceRootID
	}
	if strings.TrimSpace(task.WorkspacePath) == "" && strings.TrimSpace(scope.WorkspacePath) != "" {
		task.WorkspacePath = scope.WorkspacePath
	}
	hasRoots := scope.HasRoots || strings.TrimSpace(scope.WorkspacePath) != "" || strings.TrimSpace(task.WorkspacePath) != ""
	if task.EffectiveScope().IsWrite() && !hasRoots {
		return fmt.Errorf("write scope requires project roots")
	}
	if task.ExecutionTarget == "" {
		task.ExecutionTarget = DefaultExecutionTarget(cfg)
	}
	if task.ExecutionTarget == api.ExecutionTargetRunner && strings.TrimSpace(task.RunnerID) == "" {
		return fmt.Errorf("runner_id required when execution_target=runner")
	}
	if task.EffectiveScope().IsWrite() && strings.TrimSpace(task.OverlayID) == "" && strings.TrimSpace(task.ID) != "" {
		task.OverlayID = task.ID
	}
	if task.MaxToolLoops <= 0 {
		task.MaxToolLoops = spawn.DefaultWorkerToolBudget().Default
	}
	return nil
}

func normalizeWorkerInstructions(task *api.WorkerTask) error {
	task.Prompt = strings.TrimSpace(task.Prompt)
	if task.Prompt == "" {
		return fmt.Errorf("worker prompt required")
	}
	task.Brief = strings.TrimSpace(task.Brief)
	if task.Brief == "" {
		return fmt.Errorf("worker brief required")
	}
	return nil
}
