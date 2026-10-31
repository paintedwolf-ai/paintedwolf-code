package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/keylock"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// sqlQueueGetTimeout bounds lookups without caller contexts.
const sqlQueueGetTimeout = 3 * time.Second

type sqlQueueConfig struct {
	maxRunning       int
	defaultTarget    api.ExecutionTarget
	onRunningCancel  func(ctx context.Context, jobID string) error
	workerWorkspace  WorkerWorkspaceManager
	projects         ProjectStore
	events           *events.Publisher
	workflowRuns     *WorkflowDomains
	sessionAdmission func(ctx context.Context, sessionID string, fn func() error) error
	failureCatalog   ExecuteFailureRenderer
}

type sqlQueueCore struct {
	sqlQueueConfig
	self          *SQLQueue
	branchClaims  keylock.Group
	baselines     *workspacebaseline.Store
	db            db.Handle
	store         *SQLStore
	mu            sync.Mutex
	cancelReports ChangeReportDeps
	preparations  map[string]*api.WorkspacePreparation
	runnable      *runnableSignal
}

type sqlQueueClaims struct {
	*sqlQueueCore
}

type sqlQueuePromotion struct {
	*sqlQueueCore
}

// SQLQueue persists worker tasks.
type SQLQueue struct {
	*sqlQueueCore
	sqlQueueClaims
	sqlQueuePromotion
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
func (q *sqlQueueCore) WithTaskAdmission(ctx context.Context, task api.WorkerTask, fn func() error) error {
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
	core := &sqlQueueCore{
		sqlQueueConfig: sqlQueueConfig{
			maxRunning:    maxRunning,
			defaultTarget: api.ExecutionTargetLocal,
		},
		db:           database,
		store:        NewSQLStore(database),
		preparations: make(map[string]*api.WorkspacePreparation),
		runnable:     newRunnableSignal(),
	}
	q := &SQLQueue{
		sqlQueueCore:      core,
		sqlQueueClaims:    sqlQueueClaims{sqlQueueCore: core},
		sqlQueuePromotion: sqlQueuePromotion{sqlQueueCore: core},
	}
	core.self = q
	return q
}

func (q *sqlQueueCore) NotifyRunnable() {
	if q != nil {
		q.runnable.NotifyRunnable()
	}
}

func (q *sqlQueueCore) RunnableWake() <-chan struct{} {
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

// ErrWorkerBudgetNotLive reports a job that settled before its budget changed.
var ErrWorkerBudgetNotLive = errors.New("worker job is not pending or running")

// BudgetLedger records worker budget requests and coordinator answers on the
// job. A live job's open request ends only by Grant, which raises the ceiling
// in the same write, or by Decline, which leaves it; the worker loop tells the
// two apart by that ceiling.
type BudgetLedger interface {
	// Request records a live job's single open request; false when one is already open.
	Request(ctx context.Context, jobID string, req api.WorkerBudgetRequest) (bool, error)
	// Grant raises a live job's ceiling and answers its open request.
	Grant(ctx context.Context, childSessionID, jobID string, max int) error
	// Decline closes a live job's open request; false when none is open.
	Decline(ctx context.Context, jobID string) (bool, error)
}

// SQLBudgetLedger keeps the child session ceiling and the job row in one transaction.
type SQLBudgetLedger struct {
	sessions *sessionstore.SQL
	queue    *SQLQueue
}

// NewSQLBudgetLedger creates the durable worker budget ledger.
func NewSQLBudgetLedger(sessions *sessionstore.SQL, queue *SQLQueue) *SQLBudgetLedger {
	return &SQLBudgetLedger{sessions: sessions, queue: queue}
}

func (l *SQLBudgetLedger) configured() error {
	if l == nil || l.sessions == nil || l.queue == nil || l.sessions.DB() == nil || l.sessions.DB() != l.queue.db {
		return fmt.Errorf("worker budget ledger not configured")
	}
	return nil
}

// Request records the worker's ask and publishes the job so Den shows it.
func (l *SQLBudgetLedger) Request(ctx context.Context, jobID string, req api.WorkerBudgetRequest) (bool, error) {
	if err := l.configured(); err != nil {
		return false, err
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return false, fmt.Errorf("worker job id required")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return false, fmt.Errorf("encode worker budget request: %w", err)
	}
	recorded := false
	err = l.queue.store.inTx(ctx, func(q *db.Queries, tx *sql.Tx) error {
		rows, err := q.RequestWorkerJobBudget(ctx, db.RequestWorkerJobBudgetParams{BudgetRequestJson: string(raw), ID: jobID})
		if err != nil {
			return err
		}
		if rows != 1 {
			return nil
		}
		recorded = true
		return jobstate.EnqueueJobEventTx(ctx, tx, l.queue.store.outbox, jobID)
	})
	if err != nil || !recorded {
		return false, err
	}
	l.queue.store.notify()
	l.queue.PublishEnqueued(ctx, jobID)
	return true, nil
}

// Grant commits a raised ceiling on the child session and the job together.
func (l *SQLBudgetLedger) Grant(ctx context.Context, childSessionID, jobID string, max int) error {
	if err := l.configured(); err != nil {
		return err
	}
	childSessionID = strings.TrimSpace(childSessionID)
	jobID = strings.TrimSpace(jobID)
	if childSessionID == "" || jobID == "" || max <= 0 {
		return fmt.Errorf("worker budget identity and positive max required")
	}
	err := l.sessions.UpdateSessionWith(ctx, childSessionID, func(sess *api.Session) {
		sess.MaxToolLoops = max
	}, func(tx *sql.Tx) error {
		rows, err := db.New(tx).GrantWorkerJobBudget(ctx, db.GrantWorkerJobBudgetParams{
			MaxToolLoops: sql.NullInt64{Int64: int64(max), Valid: true}, ID: jobID,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrWorkerBudgetNotLive
		}
		return jobstate.EnqueueJobEventTx(ctx, tx, l.queue.store.outbox, jobID)
	})
	if err != nil {
		return err
	}
	l.queue.store.notify()
	l.queue.PublishEnqueued(ctx, jobID)
	return nil
}

// Decline closes the open request and publishes the job so Den drops it.
func (l *SQLBudgetLedger) Decline(ctx context.Context, jobID string) (bool, error) {
	if err := l.configured(); err != nil {
		return false, err
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return false, fmt.Errorf("worker job id required")
	}
	won, err := l.queue.store.casInTx(ctx, jobID, func(q *db.Queries) (int64, error) {
		return q.DeclineWorkerJobBudget(ctx, jobID)
	})
	if err != nil || !won {
		return false, err
	}
	l.queue.PublishEnqueued(ctx, jobID)
	return true, nil
}
