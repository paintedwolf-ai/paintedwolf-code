package project

import (
	"context"
	"database/sql"

	wire "github.com/lycaon/lycaon/pkg/api"
)

type projectEventOutbox interface {
	EnqueueProjectTx(context.Context, *sql.Tx, string, any) error
	Notify()
}

// SetEventOutbox makes project lifecycle mutations and their wire events one commit.
func (r *SQLRegistry) SetEventOutbox(outbox projectEventOutbox) {
	if r != nil {
		r.outbox = outbox
	}
}

// MutationEventsOutboxed reports whether mutations include outbox delivery.
func (r *SQLRegistry) MutationEventsOutboxed() bool {
	return r != nil && r.outbox != nil
}

func (r *SQLRegistry) enqueueProjectEventTx(
	ctx context.Context,
	tx *sql.Tx,
	action wire.ProjectEventAction,
	p *Project,
) error {
	if r == nil || r.outbox == nil || p == nil {
		return nil
	}
	event := wire.ProjectEvent{ID: p.ID, Action: action}
	if action != wire.ProjectEventDeleted {
		apiProject := ToAPI(p)
		event.Project = &apiProject
	}
	return r.outbox.EnqueueProjectTx(ctx, tx, p.ID, event)
}

func (r *SQLRegistry) notifyProjectEvents() {
	if r != nil && r.outbox != nil {
		r.outbox.Notify()
	}
}
