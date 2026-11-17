package orchestration

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// TopologyBatchStatus describes batch-level settlement for a topology run.
type TopologyBatchStatus string

const (
	TopologyBatchActive  TopologyBatchStatus = "active"
	TopologyBatchDone    TopologyBatchStatus = "done"
	TopologyBatchFailed  TopologyBatchStatus = "failed"
	TopologyBatchAborted TopologyBatchStatus = "aborted"
)

// TopologyBatch groups the planned worker units of one topology run.
type TopologyBatch struct {
	ID              string
	SessionID       string
	ProjectID       string
	WorkspacePath   string
	Task            string
	Pattern         TopologyPattern
	WorkflowID      string
	WorkflowVersion string
	WorkflowRunID   string
	BlueprintPath   string
	Status          TopologyBatchStatus
	Reason          string
}

// TopologyUnit is one worker-backed unit in a topology batch.
type TopologyUnit struct {
	ID            string
	WorkID        string
	AgentType     string
	ParentID      string
	Prompt        string
	Phase         string
	WorkspaceID   string
	WorkspaceRoot string
	WorkerID      string
	JobID         string
	Status        api.LegStatus
	Summary       string
}

// TopologyWorkers plans, dispatches, observes, and cancels worker-backed topology batches.
type TopologyWorkers interface {
	CreateBatch(ctx context.Context, batch TopologyBatch, units []TopologyUnit) (*TopologyBatch, []TopologyUnit, error)
	BatchBySession(ctx context.Context, sessionID string) (string, bool, error)
	BatchByWorkflowRun(ctx context.Context, workflowRunID string) (string, bool, error)
	GetBatch(ctx context.Context, batchID string) (*TopologyBatch, error)
	GetUnit(ctx context.Context, batchID, unitID string) (*TopologyUnit, error)
	ListUnits(ctx context.Context, batchID string) ([]TopologyUnit, error)
	UpdateUnit(ctx context.Context, batchID string, unit TopologyUnit) error
	DispatchUnit(ctx context.Context, batchID, unitID string) (*TopologyUnit, error)
	AbortBatch(ctx context.Context, batchID, reason string) error
}

// TopologyDirectSettler settles a topology unit synchronously for harness fixtures.
type TopologyDirectSettler interface {
	DispatchSettled(ctx context.Context, batchID, unitID string, result api.WorkerResult) (*TopologyUnit, error)
}

// RunWorkerStore reads the statuses of workers bound to a workflow run.
type RunWorkerStore interface {
	WorkflowWorkStatuses(ctx context.Context, workflowRunID string) (map[string]api.LegStatus, error)
}
