package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

// workflowScanInventory reads a run's bound scans for the workflow layer.
type workflowScanInventory struct {
	store *scan.SQLStore
}

func (i workflowScanInventory) RunScans(ctx context.Context, runID string) ([]api.CodeScan, error) {
	return i.store.ListByWorkflowRunID(ctx, runID)
}

func (i workflowScanInventory) Scan(ctx context.Context, scanID string) (*api.CodeScan, error) {
	return i.store.Get(ctx, scanID)
}
