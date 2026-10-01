package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

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
		return EnqueueJobEventTx(ctx, tx, l.queue.store.outbox, jobID)
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
		return EnqueueJobEventTx(ctx, tx, l.queue.store.outbox, jobID)
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
