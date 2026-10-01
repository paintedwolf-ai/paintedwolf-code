package cadence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

type dispatchExecution struct {
	Manifest    api.ScanExecutionManifest
	Fingerprint string
}

func (c *Service) resolveExecution(ctx context.Context, canonical string, row scanbase.SeriesRow) (dispatchExecution, error) {
	contract, err := scanbase.SelectedScannerContract(ctx, c.Registry, canonical, row.ScannerID, row.Categories...)
	if err != nil {
		return dispatchExecution{}, err
	}
	manifest, fingerprint, err := scancatalog.ExecutionManifest(contract)
	if err != nil {
		return dispatchExecution{}, err
	}
	return dispatchExecution{Manifest: manifest, Fingerprint: fingerprint}, nil
}

func (c *Service) dispatchExecutions(ctx context.Context, canonical string, claimed []scanbase.SeriesRow) (map[string]dispatchExecution, map[string]error) {
	executions := make(map[string]dispatchExecution, len(claimed))
	failures := make(map[string]error)
	for _, row := range claimed {
		execution, err := c.resolveExecution(ctx, canonical, row)
		if err != nil {
			failures[row.ScannerID] = err
			continue
		}
		executions[row.ScannerID] = execution
	}
	return executions, failures
}

// dispatchTargetSelections chooses full scans for pass members and deltas
// for automatic scans. Scanners without a base record one without scanning.
func (c *Service) dispatchTargetSelections(ctx context.Context, claimed []scanbase.SeriesRow, snapshot sourcesnapshot.Snapshot, executions map[string]dispatchExecution) (map[string]dispatchSelection, error) {
	out := make(map[string]dispatchSelection, len(claimed))
	store := c.Coordinator.SnapshotStore()
	generations := make(map[string]scanbase.SourceGeneration)
	for _, row := range claimed {
		if _, captured := executions[row.ScannerID]; !captured {
			continue
		}
		if row.DispatchPassID != "" {
			selection := scanbase.FullTargetSelection(snapshot)
			out[row.ScannerID] = dispatchSelection{target: &selection}
			continue
		}
		if store == nil || row.LastCoveredSnapshotID == "" {
			out[row.ScannerID] = dispatchSelection{baseline: true}
			continue
		}
		// Execution changes preserve the source baseline for exact deltas.
		// Coverage marks prior findings stale until a full scan refreshes them.
		generation, diffed := generations[row.LastCoveredSnapshotID]
		if !diffed {
			previous, err := store.Get(ctx, row.LastCoveredSnapshotID)
			if errors.Is(err, sourcesnapshot.ErrSnapshotNotFound) {
				out[row.ScannerID] = dispatchSelection{baseline: true}
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("load prior source generation: %w", err)
			}
			if generation, err = scanbase.DiffSourceGenerations(ctx, store, previous, snapshot); err != nil {
				return nil, err
			}
			generations[row.LastCoveredSnapshotID] = generation
		}
		if len(generation.UpsertedPaths) == 0 && len(generation.DeletedPaths) == 0 {
			out[row.ScannerID] = dispatchSelection{}
			continue
		}
		selection := scanbase.GenerationTargetSelection(generation)
		out[row.ScannerID] = dispatchSelection{target: &selection}
	}
	return out, nil
}

// A nil target with baseline set records a delta base without running a scanner.
type dispatchSelection struct {
	target   *scanbase.TargetSelection
	baseline bool
}

func splitSelections(claimed []scanbase.SeriesRow, selections map[string]dispatchSelection) (unchanged, baselined, dispatching []scanbase.SeriesRow) {
	for _, row := range claimed {
		selection, decided := selections[row.ScannerID]
		switch {
		case !decided:
			// No execution identity; the enqueue loop releases it with the reason.
			dispatching = append(dispatching, row)
		case selection.baseline:
			baselined = append(baselined, row)
		case selection.target == nil:
			unchanged = append(unchanged, row)
		default:
			dispatching = append(dispatching, row)
		}
	}
	return unchanged, baselined, dispatching
}

func (c *Service) finishBaselineDispatches(ctx context.Context, rows []scanbase.SeriesRow, snapshot sourcesnapshot.Snapshot, executions map[string]dispatchExecution) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, frozen := range rows {
		row, err := c.Store.GetSeries(ctx, frozen.CanonicalPath, frozen.ScannerID)
		if err != nil || row == nil || row.DispatchToken != frozen.DispatchToken {
			continue
		}
		releaseClaim(row)
		row.LastCoveredSnapshotID = snapshot.ID
		row.LastCoveredExecutionFingerprint = executions[row.ScannerID].Fingerprint
		row.LastFileCount = snapshot.FileCount
		c.rearmHeldDesireLocked(row)
		row.UpdatedAt = c.now()
		if err := c.Store.UpsertSeries(ctx, *row); err != nil {
			slog.WarnContext(ctx, "record scan series base", "scanner_id", row.ScannerID, "error", err)
		}
	}
}

func assessmentTarget(snapshot sourcesnapshot.Snapshot, rows []scanbase.SeriesRow, selections map[string]dispatchSelection) scanbase.TargetSelection {
	union := scanbase.TargetSelection{
		Kind: api.ScanTargetPaths, CaptureQuality: string(snapshot.Quality), AdmissionMode: string(snapshot.AdmissionMode),
	}
	for _, row := range rows {
		selection := selections[row.ScannerID].target
		if selection == nil {
			continue
		}
		union.Paths = unionSorted(union.Paths, selection.Paths)
		union.DeletedPaths = unionSorted(union.DeletedPaths, selection.DeletedPaths)
	}
	return union
}

func targetSummary(claimed []scanbase.SeriesRow, selections map[string]dispatchSelection) string {
	parts := make([]string, 0, len(claimed))
	for _, row := range claimed {
		selection := selections[row.ScannerID].target
		if selection == nil {
			continue
		}
		if selection.Kind == api.ScanTargetFull {
			parts = append(parts, row.ScannerID+":full")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:paths(%d+%d)", row.ScannerID, len(selection.Paths), len(selection.DeletedPaths)))
	}
	return strings.Join(parts, ",")
}

func (c *Service) finishNoopDispatches(ctx context.Context, claimed []scanbase.SeriesRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, frozen := range claimed {
		row, err := c.Store.GetSeries(ctx, frozen.CanonicalPath, frozen.ScannerID)
		if err != nil || row == nil || row.DispatchToken != frozen.DispatchToken {
			continue
		}
		releaseClaim(row)
		c.rearmHeldDesireLocked(row)
		row.UpdatedAt = c.now()
		if err := c.Store.UpsertSeries(ctx, *row); err != nil {
			slog.WarnContext(ctx, "settle unchanged scan generation", "scanner_id", row.ScannerID, "error", err)
		}
	}
}
