package jobstate

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// JobEventOutbox is the durable event surface a worker job transition needs.
// The workflow store holds, releases and cancels the same rows, so it announces
// through this too.
type JobEventOutbox interface {
	EnqueueTx(ctx context.Context, tx *sql.Tx, topic api.EventTopic, key events.PublishKey, data any) error
	Notify()
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
	task, err := FromRow(ctx, tx, row)
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
	return outbox.EnqueueTx(ctx, tx, api.EventTopicWorker, EventKey(task), JobEvent(task))
}

// EventKey scopes one job's transition. The job id facet keeps a debounce
// window from coalescing two workers under one coordinator session.
func EventKey(task api.WorkerTask) events.PublishKey {
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
