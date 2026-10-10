package worker

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// sqlQueueGetTimeout bounds lookups without caller contexts.
const sqlQueueGetTimeout = 3 * time.Second

// SQLQueue persists worker tasks.
type SQLQueue struct {
	branchClaims     keylock.Group
	baselines        *workspacebaseline.Store
	db               db.Handle
	store            *SQLStore
	mu               sync.Mutex
	maxRunning       int
	defaultTarget    api.ExecutionTarget
	onRunningCancel  func(ctx context.Context, jobID string) error
	workerWorkspace  WorkerWorkspaceManager
	projects         ProjectStore
	events           *events.Publisher
	workflowRuns     *WorkflowDomains
	sessionAdmission func(ctx context.Context, sessionID string, fn func() error) error
	failureCatalog   ExecuteFailureRenderer
	cancelReports    ChangeReportDeps
	preparations     map[string]*api.WorkspacePreparation
	runnable         *runnableSignal
}

// SetCancellationReports wires observations for cancellation and crash recovery.
func (q *SQLQueue) SetCancellationReports(deps ChangeReportDeps) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelReports = deps
}

// SetSessionAdmission wires session admission.
func (q *SQLQueue) SetSessionAdmission(admit func(ctx context.Context, sessionID string, fn func() error) error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sessionAdmission = admit
}

// WithTaskAdmission guards worker admission against session stop.
func (q *SQLQueue) WithTaskAdmission(ctx context.Context, task api.WorkerTask, fn func() error) error {
	q.mu.Lock()
	admit := q.sessionAdmission
	q.mu.Unlock()
	sessionID := strings.TrimSpace(task.ParentSessionID)
	if admit == nil || sessionID == "" {
		return fn()
	}
	return admit(ctx, sessionID, fn)
}

// SetFailureRenderer wires user-notice rendering for failed worker execute copy.
func (q *SQLQueue) SetFailureRenderer(renderer ExecuteFailureRenderer) {
	if q != nil {
		q.failureCatalog = renderer
	}
}

// NewSQLQueue creates a persistent worker queue.
func NewSQLQueue(database db.Handle, maxRunning int) *SQLQueue {
	if maxRunning <= 0 {
		maxRunning = MaxWorkers
	}
	return &SQLQueue{
		db:            database,
		store:         NewSQLStore(database),
		maxRunning:    maxRunning,
		defaultTarget: api.ExecutionTargetLocal,
		preparations:  make(map[string]*api.WorkspacePreparation),
		runnable:      newRunnableSignal(),
	}
}

func (q *SQLQueue) NotifyRunnable() {
	if q != nil {
		q.runnable.NotifyRunnable()
	}
}

func (q *SQLQueue) RunnableWake() <-chan struct{} {
	if q == nil {
		return nil
	}
	return q.runnable.RunnableWake()
}

// SetWorkersConfig applies enqueue defaults.
func (q *SQLQueue) SetWorkersConfig(cfg WorkersConfig) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.defaultTarget = DefaultExecutionTarget(cfg)
	if cfg.Poller.MaxConcurrency > 0 {
		q.maxRunning = cfg.Poller.MaxConcurrency
	}
}

// SetWorkerWorkspaceManager provisions write-worker branches on first use.
func (q *SQLQueue) SetWorkerWorkspaceManager(ws WorkerWorkspaceManager) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.workerWorkspace = ws
}

// SetProjectStore loads project roots when provisioning worker sandboxes.
func (q *SQLQueue) SetProjectStore(store ProjectStore) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.projects = store
}

// SetRunningCancel is invoked when canceling a running job.
func (q *SQLQueue) SetRunningCancel(fn func(ctx context.Context, jobID string) error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onRunningCancel = fn
}

// SetEventPublisher wires the derived board snapshot refresh.
func (q *SQLQueue) SetEventPublisher(p *events.Publisher) {
	q.events = p
}

// SetEventOutbox makes each job transition and its wire event one commit.
func (q *SQLQueue) SetEventOutbox(outbox jobstate.JobEventOutbox) {
	if q != nil && q.store != nil {
		q.store.SetEventOutbox(outbox)
	}
}

// SetWorkflowDomains gates enqueue when a workflow run is paused or terminal.
func (q *SQLQueue) SetWorkflowDomains(domains *WorkflowDomains) {
	q.workflowRuns = domains
}

func (q *SQLQueue) SetBaselineStore(store *workspacebaseline.Store) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.baselines = store
}
