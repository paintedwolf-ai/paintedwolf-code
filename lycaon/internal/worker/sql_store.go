package worker

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
)

// SQLStore persists worker jobs.
type SQLStore struct {
	db      db.Handle
	queries *db.Queries
	outbox  jobstate.JobEventOutbox
}

// NewSQLStore creates a worker job store.
func NewSQLStore(database db.Handle) *SQLStore {
	return &SQLStore{db: database, queries: db.New(database)}
}

// SetEventOutbox makes a job transition and its wire event one commit.
func (s *SQLStore) SetEventOutbox(outbox jobstate.JobEventOutbox) {
	if s != nil {
		s.outbox = outbox
	}
}

func (s *SQLStore) emitJobTx(ctx context.Context, tx *sql.Tx, jobID string) error {
	if s == nil {
		return nil
	}
	return jobstate.EnqueueJobEventTx(ctx, tx, s.outbox, jobID)
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
