package worker

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// SQLStore persists worker jobs.
type SQLStore struct {
	db      db.Handle
	queries *db.Queries
	outbox  JobEventOutbox
}

// NewSQLStore creates a worker job store.
func NewSQLStore(database db.Handle) *SQLStore {
	return &SQLStore{db: database, queries: db.New(database)}
}

// JobEventOutbox is the durable event surface a worker job transition needs.
// The workflow store holds, releases and cancels the same rows, so it announces
// through this too.
type JobEventOutbox interface {
	EnqueueTx(ctx context.Context, tx *sql.Tx, topic api.EventTopic, key events.PublishKey, data any) error
	Notify()
}

// SetEventOutbox makes a job transition and its wire event one commit.
func (s *SQLStore) SetEventOutbox(outbox JobEventOutbox) {
	if s != nil {
		s.outbox = outbox
	}
}

// EnqueueJobEventTx rereads the job and stages its event in the mutation transaction.
func EnqueueJobEventTx(ctx context.Context, tx *sql.Tx, outbox JobEventOutbox, jobID string) error {
	if outbox == nil || tx == nil {
		return nil
	}
	if err := enqueueSingleJobEventTx(ctx, tx, outbox, jobID); err != nil {
		return err
	}
	dependents, err := db.New(tx).ListWorkerDependents(ctx, jobID)
	if err != nil {
		return err
	}
	for _, id := range dependents {
		if err := enqueueSingleJobEventTx(ctx, tx, outbox, id); err != nil {
			return err
		}
	}
	return nil
}

func enqueueSingleJobEventTx(ctx context.Context, tx *sql.Tx, outbox JobEventOutbox, jobID string) error {
	row, err := db.New(tx).GetWorkerJob(ctx, jobID)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	task, err := workerTaskFromRow(ctx, tx, row)
	if err != nil {
		return err
	}
	return enqueueTask(ctx, tx, outbox, *task)
}

// EnqueueRunJobEventsTx stages one event per job bound to a workflow run, for
// the bulk hold, release, and cancel transitions a run drives.
func EnqueueRunJobEventsTx(ctx context.Context, tx *sql.Tx, outbox JobEventOutbox, workflowRunID string) error {
	if outbox == nil || tx == nil {
		return nil
	}
	rows, err := db.New(tx).ListWorkflowWorkerJobs(ctx, db.ListWorkflowWorkerJobsParams{WorkflowRunID: db.NullString(workflowRunID), StatusesJson: "[]"})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := EnqueueJobEventTx(ctx, tx, outbox, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func enqueueTask(ctx context.Context, tx *sql.Tx, outbox JobEventOutbox, task api.WorkerTask) error {
	return outbox.EnqueueTx(ctx, tx, api.EventTopicWorker, jobEventKey(task), JobEvent(task))
}

// jobEventKey scopes one job's transition. The job id facet keeps a debounce
// window from coalescing two workers under one coordinator session.
func jobEventKey(task api.WorkerTask) events.PublishKey {
	return events.PublishKey{Project: task.ProjectID, Session: task.ParentSessionID, Facet: task.ID}
}

// JobEvent projects a job row onto its wire event.
func JobEvent(task api.WorkerTask) api.WorkerEvent {
	return api.WorkerEvent{
		WorkerID:             task.ID,
		Dependencies:         task.Dependencies,
		Status:               task.Status,
		AgentType:            task.AgentType,
		ChildSessionID:       task.ChildSessionID,
		ParentSessionID:      task.ParentSessionID,
		Brief:                task.Brief,
		MergeStatus:          task.MergeStatus,
		MaxToolLoops:         task.MaxToolLoops,
		BudgetRequest:        task.BudgetRequest,
		ToolLoopsUsed:        task.ToolLoopsUsed,
		ToolCallsUsed:        task.ToolCallsUsed,
		ContextUsage:         task.ContextUsage,
		WorkspacePreparation: task.WorkspacePreparation,
		Result:               task.Result,
		Failure:              task.Failure,
		Error:                task.Error,
	}
}

func (s *SQLStore) emitJobTx(ctx context.Context, tx *sql.Tx, jobID string) error {
	if s == nil {
		return nil
	}
	return EnqueueJobEventTx(ctx, tx, s.outbox, jobID)
}

func (s *SQLStore) notify() {
	if s != nil && s.outbox != nil {
		s.outbox.Notify()
	}
}

// inTx commits the mutation and its staged event together, then wakes delivery.
func (s *SQLStore) inTx(ctx context.Context, fn func(qtx *db.Queries, tx *sql.Tx) error) error {
	return db.InTx(ctx, s.db, s.queries, s.notify, fn)
}

// casInTx emits an event only for the winning transition.
func (s *SQLStore) casInTx(ctx context.Context, jobID string, mutate func(q *db.Queries) (int64, error)) (bool, error) {
	won := false
	err := s.inTx(ctx, func(q *db.Queries, tx *sql.Tx) error {
		n, err := mutate(q)
		if err != nil {
			return err
		}
		won = n == 1
		if !won {
			return nil
		}
		return s.emitJobTx(ctx, tx, jobID)
	})
	return won && err == nil, err
}

// mutateInTx runs one unconditional job write and announces the result.
func (s *SQLStore) mutateInTx(ctx context.Context, jobID string, mutate func(q *db.Queries) error) error {
	return s.inTx(ctx, func(q *db.Queries, tx *sql.Tx) error {
		if err := mutate(q); err != nil {
			return err
		}
		return s.emitJobTx(ctx, tx, jobID)
	})
}
