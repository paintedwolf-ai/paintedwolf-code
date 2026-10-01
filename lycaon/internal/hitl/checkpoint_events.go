package hitl

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// EventsViaOutbox reports whether committed mutations own event delivery.
func (s *SQLStore) EventsViaOutbox() bool { return s.outbox != nil }

// Worker registration and checkpoint changes share commit order. A child
// approval must never arrive before the worker that gives it a parent view.
func (s *SQLStore) enqueueCheckpointTx(ctx context.Context, tx *sql.Tx, row StoredCheckpoint) error {
	if s.outbox == nil {
		return nil
	}
	return s.outbox.EnqueueTx(ctx, tx, api.EventTopicCheckpoint,
		events.PublishKey{Project: row.ProjectID, Session: row.SessionID}, StoredCheckpointToEvent(row))
}
