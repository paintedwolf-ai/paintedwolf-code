package workflow

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// enqueueRunTx commits a workflow revision with its stream event.
func (s *SQLStore) enqueueRunTx(ctx context.Context, tx *sql.Tx, run *api.WorkflowRun, event api.WorkflowEventKind, previousPhase string) error {
	if run == nil {
		return nil
	}
	if run.Revision <= 0 {
		return fmt.Errorf("workflow event revision must be positive")
	}
	ev := api.WorkflowEvent{
		Event:         event,
		WorkflowID:    run.WorkflowID,
		WorkflowRunID: run.ID,
		Run:           run,
		PreviousPhase: previousPhase,
		Phase:         run.CurrentPhase,
		Status:        string(run.Status),
	}
	return s.outbox.EnqueueTx(ctx, tx, api.EventTopicWorkflow, events.PublishKey{
		Project:        run.ProjectID,
		Session:        run.SessionID,
		Facet:          run.ID,
		EntityRevision: uint64(run.Revision),
	}, ev)
}
