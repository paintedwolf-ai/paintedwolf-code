package scanning

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

// BindMovedFiles binds finished scans to running workflow runs whose file moves match.
func BindMovedFiles(ctx context.Context, store *scan.SQLStore, runs []api.WorkflowRun, completed api.CodeScan) error {
	if store == nil || completed.Status != api.CodeScanStatusComplete {
		return nil
	}
	var errs []error
	for _, run := range runs {
		bound, err := store.ListByWorkflowRunID(ctx, run.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(bound) == 0 || bound[0].CanonicalPath != completed.CanonicalPath || !scan.ClosesMovedFiles(bound, completed) {
			continue
		}
		if err := store.BindWorkflowRun(ctx, completed.ID, run.ID); err != nil {
			errs = append(errs, fmt.Errorf("bind scan %s to run %s: %w", completed.ID, run.ID, err))
		}
	}
	return errors.Join(errs...)
}

// ReconcileTerminals notifies the workflow layer of terminal scan obligations.
func ReconcileTerminals(ctx context.Context, store *scan.SQLStore, scanID string, recordTerminal func(context.Context, string, string) error) error {
	if store == nil || recordTerminal == nil {
		return nil
	}
	bindings, err := store.PendingTerminalWorkflowBindings(ctx, scanID, 256)
	if err != nil {
		return err
	}
	var errs []error
	for _, binding := range bindings {
		if err := recordTerminal(ctx, binding.WorkflowRunID, scan.WorkflowObligationKind); err != nil {
			errs = append(errs, fmt.Errorf("scan %s workflow %s: %w", binding.ScanID, binding.WorkflowRunID, err))
			continue
		}
		if err := store.MarkWorkflowTerminalNotified(ctx, binding.ScanID, binding.WorkflowRunID); err != nil {
			errs = append(errs, fmt.Errorf("acknowledge scan %s workflow %s: %w", binding.ScanID, binding.WorkflowRunID, err))
		}
	}
	return errors.Join(errs...)
}
