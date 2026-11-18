package worker

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// sqlQueueGetTimeout bounds lookups without caller contexts.
const sqlQueueGetTimeout = 3 * time.Second

// CriteriaEvaluator computes JSON-encoded criterion outcomes before a worker round settles.
type CriteriaEvaluator func(
	ctx context.Context,
	task *api.WorkerTask,
	projectDir string,
	jobComplete bool,
	summary string,
	divergedPaths []string,
	report *api.WorkerChangeReport,
) string

// SQLQueue persists admitted worker rounds and their outcomes.
type SQLQueue struct {
	Branches          *BranchService
	Promotions        *PromotionStore
	maxRunning        int
	defaultTarget     api.ExecutionTarget
	onRunningCancel   func(context.Context, string) error
	activeExecution   func(string) bool
	workflowRuns      *WorkflowDomains
	sessionAdmission  func(context.Context, string, func() error) error
	failureCatalog    ExecuteFailureRenderer
	criteriaEvaluator CriteriaEvaluator
	onRoundSettled    func(context.Context, string)
	db                db.Handle
	store             *SQLStore
	mu                sync.Mutex
	cancelReports     ChangeReportDeps
	runnable          *runnableSignal
	changes           *jobChanges
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

// SetRoundDeliveryHooks wires typed criteria evaluation and post-settlement closeout notification.
func (q *SQLQueue) SetRoundDeliveryHooks(
	eval CriteriaEvaluator,
	onSettled func(ctx context.Context, parentSessionID string),
) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.criteriaEvaluator = eval
	q.onRoundSettled = onSettled
}

// NewSQLQueue creates a persistent worker queue.
func NewSQLQueue(database db.Handle, maxRunning int) *SQLQueue {
	if maxRunning <= 0 {
		maxRunning = MaxWorkers
	}
	store := NewSQLStore(database)
	changes := &jobChanges{store: store}
	runnable := newRunnableSignal()
	q := &SQLQueue{
		db: database, store: store, changes: changes, runnable: runnable,
		maxRunning: maxRunning, defaultTarget: api.ExecutionTargetLocal,
		Branches:   &BranchService{store: store, changes: changes, preparations: make(map[string]*api.WorkspacePreparation)},
		Promotions: &PromotionStore{store: store, changes: changes, runnable: runnable},
	}

	return q
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

// SetRunningCancel is invoked when canceling a running job.
func (q *SQLQueue) SetRunningCancel(fn func(ctx context.Context, jobID string) error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onRunningCancel = fn
}

// SetEventPublisher wires the derived board snapshot refresh.
func (q *SQLQueue) SetEventPublisher(p *events.Publisher) {
	q.changes.publisher = p
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
