// Package worker defines the persistent worker queue and in-session worker ring.
package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskAdmission guards worker admission against session stop.
type TaskAdmission interface {
	WithTaskAdmission(ctx context.Context, task api.WorkerTask, fn func() error) error
}

// ClaimRequest selects which pending jobs a claimer may take.
type ClaimRequest struct {
	ProjectID       string
	ClaimedBy       string
	ExecutionTarget api.ExecutionTarget
}

// WorkerQueue persists worker jobs from pending through terminal state.
type WorkerQueue interface {
	TaskAdmission
	Enqueue(ctx context.Context, task api.WorkerTask) (string, error)
	PrepareEnqueue(ctx context.Context, projectID string, task *api.WorkerTask) error
	PublishEnqueued(ctx context.Context, jobID string)
	ClaimNext(ctx context.Context, req ClaimRequest) (*api.WorkerTask, error)
	Complete(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error)
	// Retry closes the current attempt and returns the semantic job to pending.
	Retry(ctx context.Context, claimed *api.WorkerTask, err error) (bool, error)
	Park(ctx context.Context, claimed *api.WorkerTask) (bool, error)
	ResumeReadyWaits(ctx context.Context) (int, error)
	Fail(ctx context.Context, claimed *api.WorkerTask, err error) (bool, error)
	RequestCancellation(ctx context.Context, id string) (bool, error)
	RequestClaimCancellation(ctx context.Context, claimed *api.WorkerTask) (bool, error)
	// StopExecution fences the job and joins runtime without discarding its workspace.
	StopExecution(ctx context.Context, id string) error
	Cancel(ctx context.Context, id string, result *api.WorkerResult) error
	FinishCanceled(ctx context.Context, id string, result *api.WorkerResult) error
	Hold(ctx context.Context, id string) error
	List(ctx context.Context, projectID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListSessionPage(ctx context.Context, query SessionPageQuery) (SessionPage, error)
	ListBySession(ctx context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListByWorkflowRunID(ctx context.Context, runID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListCancellationRequestsByRunID(ctx context.Context, runID string) ([]api.WorkerTask, error)
	ListByWorkspacePath(ctx context.Context, workspacePath string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	Get(jobID string) (*api.WorkerTask, bool)
	GetLatestByChildSessionID(ctx context.Context, childSessionID string) (*api.WorkerTask, bool)
	SetChildSessionID(ctx context.Context, jobID, childSessionID string) error
	PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) error
	RenewClaim(ctx context.Context, jobID, claimToken string) (bool, error)
	RecoverExpiredClaims(ctx context.Context) ([]api.WorkerTask, error)
	ListPendingOutcomes(ctx context.Context) ([]api.WorkerTask, error)
	MarkOutcomeDelivered(ctx context.Context, jobID string) error
	// ClaimWorkerBranch provisions a write worker's branch on first use.
	ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error)
	// EnsureWorkerBranch materializes a claimed branch and leases it to the caller.
	EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *BranchLease, error)
	// ListBranchJobs includes jobs whose branch is still being provisioned.
	ListBranchJobs(ctx context.Context) ([]BranchJob, error)
}

// admitWorkerTransition holds session admission until the claim transition commits.
func admitWorkerTransition(ctx context.Context, admission TaskAdmission, claimed *api.WorkerTask, transition func() (bool, error)) (bool, error) {
	if claimed == nil {
		return false, fmt.Errorf("claimed worker task required")
	}
	var won bool
	err := admission.WithTaskAdmission(ctx, *claimed, func() error {
		var err error
		won, err = transition()
		return err
	})
	return won, err
}

// WorkflowDomains binds independent run and task admission resources.
type WorkflowDomains struct {
	Runs  WorkflowRunAdmission
	Tasks WorkflowTaskAdmission
}

type WorkflowRunAdmission interface {
	AssertRunnable(ctx context.Context, runID string) error
}
type WorkflowTaskAdmission interface {
	AssertWorkerTask(ctx context.Context, task *api.WorkerTask) error
}

type subjectQueue interface {
	Get(string) (*api.WorkerTask, bool)
}

func captureWorkerSubject(tctx tools.ToolContext, queue subjectQueue, id string) {
	if queue == nil {
		return
	}
	task, ok := queue.Get(strings.TrimSpace(id))
	if ok && task != nil && strings.TrimSpace(task.ParentSessionID) == strings.TrimSpace(tctx.Identity.SessionID) {
		tctx.SetDisplaySubject(task.Brief)
	}
}

// filterLiveOverlays keeps the write overlays promote or reject can still
// target: in-flight write workers, and completed ones that kept a workspace and
// have no terminal merge status.
func filterLiveOverlays(tasks []api.WorkerTask) []api.WorkerTask {
	out := make([]api.WorkerTask, 0, len(tasks))
	for _, task := range tasks {
		if !task.EffectiveScope().IsWrite() {
			continue
		}
		if strings.TrimSpace(task.WorkspaceRoot) == "" && task.Status == api.WorkerStatusComplete {
			continue
		}
		switch task.MergeStatus {
		case api.WorkerMergeStatusMerged,
			api.WorkerMergeStatusOrphaned,
			api.WorkerMergeStatusRejected,
			api.WorkerMergeStatusAborted:
			continue
		case api.WorkerMergeStatusPending, api.WorkerMergeStatusApplying, api.WorkerMergeStatusRebasing:
		}
		out = append(out, task)
	}
	return out
}

// ExecuteFailureRenderer renders Den-facing worker execute failure copy.
type ExecuteFailureRenderer interface {
	RenderExecuteFailure(err error) api.WorkerFailure
}

// ExecuteFailureCode maps a worker execute error to a user-notice code.
func ExecuteFailureCode(err error) string {
	if err == nil {
		return "worker_execute_failed"
	}
	if errors.Is(err, promptloop.ErrLLMTurnTimeout) {
		return "worker_closeout_exhausted"
	}
	if errors.Is(err, workspace.ErrWorkspaceStorageExhausted) {
		return "worker_workspace_disk_full"
	}
	if errors.Is(err, ErrWorkerBranchClaimFailed) {
		return "worker_workspace_unavailable"
	}
	if errors.Is(err, promptloop.ErrOwnerUnsettled) {
		return "worker_owner_unsettled"
	}
	return "worker_execute_failed"
}

// RunnableNotifier announces newly claimable durable queue state.
type RunnableNotifier interface {
	NotifyRunnable()
}

// RunnableWakeSource exposes process-local runnable notifications to a claimer.
type RunnableWakeSource interface {
	RunnableWake() <-chan struct{}
}

type runnableSignal struct {
	wake chan struct{}
}

func newRunnableSignal() *runnableSignal {
	return &runnableSignal{wake: make(chan struct{}, 1)}
}

func (s *runnableSignal) NotifyRunnable() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *runnableSignal) RunnableWake() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.wake
}

const maxWorkerExecutionAttempts = 3

// PermanentExecutionError prevents retries for invalid durable input.
type PermanentExecutionError struct {
	Err error
}

func (e *PermanentExecutionError) Error() string {
	if e == nil || e.Err == nil {
		return "permanent worker execution failure"
	}
	return e.Err.Error()
}

func (e *PermanentExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func workerAttemptRetryable(task *api.WorkerTask, err error) bool {
	if task == nil || task.Attempt >= maxWorkerExecutionAttempts || err == nil {
		return false
	}
	var permanent *PermanentExecutionError
	var thinking *llm.ThinkingOverrideError
	if errors.As(err, &thinking) {
		return false
	}
	if errors.As(err, &permanent) || errors.Is(err, context.Canceled) || errors.Is(err, lifecycle.ErrStopping) || errors.Is(err, ErrClaimLost) {
		return false
	}
	if empty, ok := failure.AsProviderEmptyCompletion(err); ok && !empty.Retryable {
		return false
	}
	if _, ok := providerretry.AsProviderRequestRejected(err); ok {
		return false
	}
	switch {
	case errors.Is(err, failure.ErrProviderNotConfigured),
		errors.Is(err, failure.ErrProviderOutputTruncated),
		errors.Is(err, failure.ErrProviderContextTooSmall),
		errors.Is(err, failure.ErrProviderToolCallsUnsupported),
		errors.Is(err, failure.ErrProviderToolCallsInProse),
		errors.Is(err, workspace.ErrWorkspaceStorageExhausted),
		errors.Is(err, ErrWorkerBranchClaimFailed),
		errors.Is(err, promptloop.ErrOwnerUnsettled):
		return false
	default:
		return true
	}
}

// WorkersConfig configures the local worker poller and executor instance.
type WorkersConfig struct {
	Poller struct {
		MaxConcurrency        int `yaml:"max_concurrency"`
		MaintenanceIntervalMs int `yaml:"maintenance_interval_ms"`
	} `yaml:"poller"`
	Executor struct {
		InstanceID             string `yaml:"instance_id"`
		DefaultExecutionTarget string `yaml:"default_execution_target"`
	} `yaml:"executor"`
}

var (
	bundledWorkersOnce sync.Once
	bundledWorkers     WorkersConfig
	bundledWorkersErr  error
)

// DefaultWorkersConfig returns the shipped workers config.
// Fail-closed: panics if the bundled file cannot be loaded.
func DefaultWorkersConfig() WorkersConfig {
	bundledWorkersOnce.Do(func() {
		bundledWorkers, bundledWorkersErr = LoadWorkersConfig()
	})
	if bundledWorkersErr != nil {
		panic(bundledWorkersErr)
	}
	return bundledWorkers
}

// LoadWorkersConfig reads the shipped workers.yaml. Missing or invalid fails
// closed. Zero fields after unmarshal are filled from spawn caps / local
// execution defaults.
func LoadWorkersConfig() (WorkersConfig, error) {
	data, err := config.Read(config.Workers)
	if err != nil {
		return WorkersConfig{}, fmt.Errorf("read workers config: %w", err)
	}
	var cfg WorkersConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return WorkersConfig{}, fmt.Errorf("parse workers config: %w", err)
	}
	if cfg.Poller.MaxConcurrency <= 0 {
		cfg.Poller.MaxConcurrency = spawn.MaxInFlightTaskWorkers
	}
	if cfg.Poller.MaintenanceIntervalMs <= 0 {
		cfg.Poller.MaintenanceIntervalMs = 5000
	}
	if strings.TrimSpace(cfg.Executor.InstanceID) == "" {
		cfg.Executor.InstanceID = "auto"
	}
	if strings.TrimSpace(cfg.Executor.DefaultExecutionTarget) == "" {
		cfg.Executor.DefaultExecutionTarget = string(api.ExecutionTargetLocal)
	}
	return cfg, nil
}

// ResolveInstanceID returns hostname:pid when cfg says auto.
func ResolveInstanceID(cfg WorkersConfig) string {
	id := strings.TrimSpace(cfg.Executor.InstanceID)
	if id == "" || strings.EqualFold(id, "auto") {
		host, _ := os.Hostname()
		if host == "" {
			host = "local"
		}
		return fmt.Sprintf("%s:%d", host, os.Getpid())
	}
	return id
}

// MaintenanceInterval returns the recovery cadence.
func (c WorkersConfig) MaintenanceInterval() time.Duration {
	return time.Duration(c.Poller.MaintenanceIntervalMs) * time.Millisecond
}
