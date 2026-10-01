package delegation

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetEventOutbox makes a delegation mutation and its wire event one commit.
func (s *SQLStore) SetEventOutbox(outbox delegationEventOutbox) {
	if s != nil {
		s.outbox = outbox
	}
}

// delegationEventOutbox is the durable event surface the store needs. It is an
// interface so a test can refuse the write and assert the mutation rolls back.
type delegationEventOutbox interface {
	EnqueueTx(ctx context.Context, tx *sql.Tx, topic api.EventTopic, key events.PublishKey, data any) error
	Notify()
}

// emitDelegationTx stages the delegation's current state as a wire event inside
// the caller's transaction.
//
// The event is read back through the same transaction rather than assembled
// from the caller's arguments, so it describes what the row actually holds and a
// partial update cannot announce a state the store rejected.
func (s *SQLStore) emitDelegationTx(ctx context.Context, tx *sql.Tx, delegationID, legID string) error {
	if s == nil || s.outbox == nil {
		return nil
	}
	row, err := s.queries.WithTx(tx).GetDelegation(ctx, delegationID)
	if err != nil {
		return err
	}
	delegation, err := delegationFromRow(row)
	if err != nil {
		return err
	}
	return s.outbox.EnqueueTx(ctx, tx, api.EventTopicDelegation,
		events.PublishKey{Project: delegation.ProjectID, Session: delegation.CoordinatorSessionID, Facet: facet(delegationID, legID)},
		api.DelegationEvent{DelegationID: delegation.ID, Status: delegation.Status, LegID: legID, Phase: delegation.Phase})
}

// facet separates a leg's transition from its delegation's, so a debounce window
// holding both delivers both.
func facet(delegationID, legID string) string {
	if legID == "" {
		return delegationID
	}
	return delegationID + ":" + legID
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
