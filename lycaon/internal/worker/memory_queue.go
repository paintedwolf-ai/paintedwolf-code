package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrNoPendingJobs is returned when ClaimNext finds no queued work.
var ErrNoPendingJobs = errors.New("no pending worker jobs")

// MaxWorkers is the default running cap for NewInMemoryQueue.
const MaxWorkers = 48

// ErrMaxWorkers is returned when the running worker cap is reached.
var ErrMaxWorkers = errors.New("max workers reached")

type queuedJob struct {
	cancelRequested bool
	task            api.WorkerTask
	projectID       string
}

type memoryQueueConfig struct {
	maxRunning       int
	defaultTarget    api.ExecutionTarget
	onRunningCancel  func(ctx context.Context, jobID string) error
	events           *events.Publisher
	workflowRuns     *WorkflowDomains
	sessionAdmission func(ctx context.Context, sessionID string, fn func() error) error
	failureCatalog   ExecuteFailureRenderer
	workerWorkspace  WorkerWorkspaceManager
	projects         ProjectStore
}

type memoryQueueCore struct {
	memoryQueueConfig
	self             *InMemoryQueue
	branchClaims     keylock.Group
	mu               sync.Mutex
	jobs             map[string]*queuedJob
	pending          []string
	running          map[string]struct{}
	published        map[string]struct{}
	outcomeDelivered map[string]struct{}
	cancelReports    ChangeReportDeps
	runnable         *runnableSignal
}

type memoryQueueClaims struct {
	*memoryQueueCore
}

type memoryQueueQuery struct {
	*memoryQueueCore
}

// InMemoryQueue implements WorkerQueue with FIFO pending jobs and a running cap.
type InMemoryQueue struct {
	*memoryQueueCore
	memoryQueueClaims
	memoryQueueQuery
}

// SetCancellationReports wires observations for cancellation and crash recovery.
func (q *InMemoryQueue) SetCancellationReports(deps ChangeReportDeps) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelReports = deps
}

// SetSessionAdmission wires session admission.
func (q *InMemoryQueue) SetSessionAdmission(admit func(ctx context.Context, sessionID string, fn func() error) error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sessionAdmission = admit
}

// WithTaskAdmission guards worker admission against session stop.
func (q *memoryQueueCore) WithTaskAdmission(ctx context.Context, task api.WorkerTask, fn func() error) error {
	q.mu.Lock()
	admit := q.sessionAdmission
	q.mu.Unlock()
	sessionID := strings.TrimSpace(task.ParentSessionID)
	if admit == nil || sessionID == "" {
		return fn()
	}
	return admit(ctx, sessionID, fn)
}

// NewInMemoryQueue creates a queue with the given concurrent running cap.
func NewInMemoryQueue(maxRunning int) *InMemoryQueue {
	if maxRunning <= 0 {
		maxRunning = MaxWorkers
	}
	core := &memoryQueueCore{
		memoryQueueConfig: memoryQueueConfig{
			maxRunning:    maxRunning,
			defaultTarget: api.ExecutionTargetLocal,
		},
		jobs:             make(map[string]*queuedJob),
		running:          make(map[string]struct{}),
		published:        make(map[string]struct{}),
		outcomeDelivered: make(map[string]struct{}),
		runnable:         newRunnableSignal(),
	}
	q := &InMemoryQueue{
		memoryQueueCore:   core,
		memoryQueueClaims: memoryQueueClaims{memoryQueueCore: core},
		memoryQueueQuery:  memoryQueueQuery{memoryQueueCore: core},
	}
	core.self = q
	return q
}

func (q *memoryQueueCore) NotifyRunnable() {
	if q != nil {
		q.runnable.NotifyRunnable()
	}
}

func (q *memoryQueueCore) RunnableWake() <-chan struct{} {
	if q == nil {
		return nil
	}
	return q.runnable.RunnableWake()
}

// SetWorkersConfig applies enqueue defaults from workers.yaml.
func (q *InMemoryQueue) SetWorkersConfig(cfg WorkersConfig) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.defaultTarget = DefaultExecutionTarget(cfg)
	if cfg.Poller.MaxConcurrency > 0 {
		q.maxRunning = cfg.Poller.MaxConcurrency
	}
}

// SetRunningCancel wires running-job aborts.
func (q *InMemoryQueue) SetRunningCancel(fn func(ctx context.Context, jobID string) error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onRunningCancel = fn
}

// SetFailureRenderer wires user-notice rendering for failed worker execute copy.
func (q *InMemoryQueue) SetFailureRenderer(renderer ExecuteFailureRenderer) {
	if q != nil {
		q.failureCatalog = renderer
	}
}

// SetEventPublisher wires worker SSE events on queue transitions.
func (q *InMemoryQueue) SetEventPublisher(p *events.Publisher) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.events = p
}

// SetWorkflowDomains gates enqueue when a workflow run is paused or terminal.
func (q *InMemoryQueue) SetWorkflowDomains(domains *WorkflowDomains) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.workflowRuns = domains
}

// SetWorkerWorkspaceManager provisions private branches on ClaimWorkerBranch.
func (q *InMemoryQueue) SetWorkerWorkspaceManager(ws WorkerWorkspaceManager) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.workerWorkspace = ws
}

// SetProjectStore loads project roots when provisioning worker sandboxes.
func (q *InMemoryQueue) SetProjectStore(store ProjectStore) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.projects = store
}

// Cancel aborts or settles a canceled worker.
func (q *memoryQueueClaims) Cancel(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if err := q.StopExecution(ctx, jobID); err != nil {
		return err
	}
	return q.FinishCanceled(ctx, jobID, result)
}

// StopExecution retains the workspace until runtime has released it.
func (q *memoryQueueClaims) StopExecution(ctx context.Context, jobID string) error {
	requested, err := q.RequestCancellation(ctx, jobID)
	if err != nil || !requested {
		return err
	}
	q.mu.Lock()
	job := q.jobs[jobID]
	onCancel := q.onRunningCancel
	interrupt := job.task.ChildSessionID != "" || job.task.Status == api.WorkerStatusRunning || job.task.Status == api.WorkerStatusWaiting
	q.mu.Unlock()
	if interrupt && onCancel != nil {
		if err := onCancel(ctx, jobID); err != nil {
			return err
		}
	}
	return nil
}

func (q *memoryQueueClaims) RequestCancellation(ctx context.Context, jobID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[jobID]
	if !ok {
		return false, fmt.Errorf("job not found: %s", jobID)
	}
	if job.task.Status.IsTerminal() {
		return false, nil
	}
	job.cancelRequested = true
	return true, nil
}

func (q *memoryQueueClaims) RequestClaimCancellation(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if claimed == nil || claimed.ClaimToken == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[claimed.ID]
	if !ok || job.task.Status != api.WorkerStatusRunning || job.task.ClaimToken != claimed.ClaimToken || job.cancelRequested {
		return false, nil
	}
	job.cancelRequested = true
	return true, nil
}

// FinishCanceled records cancellation after execution exits.
func (q *memoryQueueClaims) FinishCanceled(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if result == nil {
		q.mu.Lock()
		deps := q.cancelReports
		q.mu.Unlock()
		result = cancellationResultForJob(ctx, q.self, jobID, deps)
	}
	return finalizeCanceled(ctx, q.self, jobID, result)
}

// Hold moves a pending job to held without terminating it.
func (q *memoryQueueClaims) Hold(ctx context.Context, jobID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("job not found: %s", jobID)
	}
	if job.task.Status != api.WorkerStatusPending || job.cancelRequested {
		q.mu.Unlock()
		return nil
	}
	job.task.Status = api.WorkerStatusHeld
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	q.refreshBoard(ctx, snapshot, projectID)
	return nil
}

func finalizeCanceled(ctx context.Context, q *InMemoryQueue, jobID string, result *api.WorkerResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("job not found: %s", jobID)
	}
	if job.task.Status.IsTerminal() {
		q.mu.Unlock()
		return nil
	}
	delete(q.running, jobID)
	job.task.Status = api.WorkerStatusCanceled
	if result != nil {
		copied := *result
		job.task.Result = &copied
	}
	now := time.Now().UTC()
	job.task.CompletedAt = &now
	job.task.Error = ""
	job.task.Failure = nil
	job.task.ClaimToken = ""
	job.task.HeartbeatAt = nil
	job.task.LeaseExpiresAt = nil
	if job.task.Result == nil {
		job.task.Result = &api.WorkerResult{Status: "canceled"}
	}
	workspaceRoot := strings.TrimSpace(job.task.WorkspaceRoot)
	job.task.MergeStatus = api.WorkerMergeStatusAborted
	job.task.WorkspaceRoot = ""
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	destroyWorkspaceRoot(q, jobID, workspaceRoot)
	q.refreshBoard(ctx, snapshot, projectID)
	return nil
}
