package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/pkg/api"
	"reflect"
	"strings"
)

var (
	errDecisionMissing     = errors.New("pending decision not found")
	errDecisionJobMismatch = errors.New("pending decision belongs to another job")
	errDecisionChanged     = errors.New("pending decision changed before resolution")
)

// DecisionStateError distinguishes an unchanged decision on a non-suspended job.
type DecisionStateError struct {
	JobID  string
	Status string
}

func (e *DecisionStateError) Error() string {
	return fmt.Sprintf("worker %s is %s, not suspended for a decision", e.JobID, e.Status)
}

// DecisionResolver commits a decision answer and worker resume.
type DecisionResolver interface {
	Resolve(context.Context, api.WorkerDecisionRequest, api.Message) error
}

// SQLDecisionResolver resolves decisions in one database transaction.
type SQLDecisionResolver struct {
	messages *sessionstore.SQL
	queue    *SQLQueue
}

func NewSQLDecisionResolver(messages *sessionstore.SQL, queue *SQLQueue) *SQLDecisionResolver {
	return &SQLDecisionResolver{messages: messages, queue: queue}
}

func (r *SQLDecisionResolver) Resolve(ctx context.Context, expected api.WorkerDecisionRequest, message api.Message) error {
	if r == nil || r.messages == nil || r.queue == nil || r.messages.DB() == nil || r.messages.DB() != r.queue.db {
		return fmt.Errorf("decision resolver not configured")
	}
	return r.resolve(ctx, expected, message)
}

func (r *SQLDecisionResolver) resolve(ctx context.Context, expected api.WorkerDecisionRequest, message api.Message) error {
	childSessionID := strings.TrimSpace(expected.ChildSessionID)
	jobID := strings.TrimSpace(expected.WorkerID)
	err := r.messages.AppendMessagesWith(ctx, childSessionID, func(tx *sql.Tx) error {
		queries := db.New(tx)
		row, err := queries.GetWorkerDecision(ctx, childSessionID)
		if db.IsNoRows(err) {
			return errDecisionMissing
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(row.JobID) != jobID {
			return errDecisionJobMismatch
		}
		var current api.WorkerDecisionRequest
		if err := json.Unmarshal([]byte(row.DecisionJson), &current); err != nil {
			return fmt.Errorf("decode decision: %w", err)
		}
		if !reflect.DeepEqual(current, expected) {
			return errDecisionChanged
		}
		resumed, err := queries.ResumeWorkerJobDecision(ctx, db.ResumeWorkerJobDecisionParams{
			ID:             jobID,
			ChildSessionID: db.NullString(childSessionID),
		})
		if err != nil {
			return err
		}
		if resumed != 1 {
			job, loadErr := queries.GetWorkerJob(ctx, jobID)
			if loadErr != nil {
				return loadErr
			}
			return &DecisionStateError{JobID: jobID, Status: job.Status}
		}
		if err := queries.ClearWorkerOutcomeDelivery(ctx, jobID); err != nil {
			return err
		}
		if err := jobstate.EnqueueJobEventTx(ctx, tx, r.queue.store.outbox, jobID); err != nil {
			return err
		}
		return queries.DeleteWorkerDecision(ctx, childSessionID)
	}, message)
	if err != nil {
		return err
	}
	r.queue.store.notify()
	r.queue.PublishEnqueued(ctx, jobID)
	r.queue.NotifyRunnable()
	return nil
}
