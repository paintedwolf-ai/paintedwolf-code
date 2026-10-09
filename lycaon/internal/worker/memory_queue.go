package worker

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
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

// InMemoryQueue implements WorkerQueue with FIFO pending jobs and a running cap.
type InMemoryQueue struct {
	branchClaims     keylock.Group
	mu               sync.Mutex
	jobs             map[string]*queuedJob
	pending          []string
	running          map[string]struct{}
	published        map[string]struct{}
	outcomeDelivered map[string]struct{}
	maxRunning       int
	defaultTarget    api.ExecutionTarget
	onRunningCancel  func(ctx context.Context, jobID string) error
	events           *events.Publisher
	workflowRuns     *WorkflowDomains
	sessionAdmission func(ctx context.Context, sessionID string, fn func() error) error
	failureCatalog   ExecuteFailureRenderer
	cancelReports    ChangeReportDeps
	workerWorkspace  WorkerWorkspaceManager
	projects         ProjectStore
	runnable         *runnableSignal
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
func (q *InMemoryQueue) WithTaskAdmission(ctx context.Context, task api.WorkerTask, fn func() error) error {
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
	return &InMemoryQueue{
		jobs:             make(map[string]*queuedJob),
		running:          make(map[string]struct{}),
		published:        make(map[string]struct{}),
		outcomeDelivered: make(map[string]struct{}),
		maxRunning:       maxRunning,
		defaultTarget:    api.ExecutionTargetLocal,
		runnable:         newRunnableSignal(),
	}
}

func (q *InMemoryQueue) NotifyRunnable() {
	if q != nil {
		q.runnable.NotifyRunnable()
	}
}

func (q *InMemoryQueue) RunnableWake() <-chan struct{} {
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
