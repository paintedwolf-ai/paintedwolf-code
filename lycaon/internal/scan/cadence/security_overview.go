package cadence

import (
	"context"
	"sort"
	"strings"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

// SecurityOverview reports the project baseline, full passes, and scanner readiness.
func (c *Service) SecurityOverview(ctx context.Context, projectDir string, labels map[string]string) (api.SecurityOverview, error) {
	out := api.SecurityOverview{Enabled: c.securityOn(), Scanners: []api.SecurityScannerState{}}
	if !c.configured() {
		return out, nil
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return out, err
	}
	series, err := c.Store.ListSeriesForPath(ctx, canonical)
	if err != nil {
		return out, err
	}
	byScanner := make(map[string]scanbase.SeriesRow, len(series))
	for _, row := range series {
		byScanner[row.ScannerID] = row
	}
	open, err := c.Store.OpenScansForPath(ctx, canonical)
	if err != nil {
		return out, err
	}
	activeScanners := make(map[string]bool, len(open))
	for _, scan := range open {
		activeScanners[scan.ScannerID] = true
	}
	passes, err := c.Store.FullPassesForPath(ctx, canonical)
	if err != nil {
		return out, err
	}
	running, last := currentFullPasses(passes)
	if running != nil {
		wire := running.Wire()
		out.Running = &wire
	}
	if last != nil {
		wire := last.Wire()
		out.LastFull = &wire
	}
	executions := make(map[string]string)
	baselineID := ""
	for _, meta := range scanbase.ListSelectedScanners(ctx, c.Registry, canonical) {
		state := api.SecurityScannerState{
			ID: meta.ID, Label: scannerLabel(meta, labels), Categories: append([]api.ScanCategory(nil), meta.Categories...),
			Available: true, Running: activeScanners[meta.ID],
		}
		// Availability and execution identity share one contract resolution.
		execution := ""
		if contract, err := scanbase.SelectedScannerContract(ctx, c.Registry, canonical, meta.ID, meta.Categories...); err != nil {
			state.Available, state.Unavailable = false, err.Error()
		} else if _, fingerprint, err := scancatalog.ExecutionManifest(contract); err != nil {
			state.Available, state.Unavailable = false, err.Error()
		} else {
			execution = fingerprint
		}
		if row, ok := byScanner[meta.ID]; ok {
			if baselineID == "" {
				baselineID = row.LastCoveredSnapshotID
			}
			applySeriesReadiness(&state, row, execution)
		}
		state.LastFullAt = lastFullAt(passes, meta.ID)
		out.Scanners = append(out.Scanners, state)
		executions[meta.ID] = execution
	}
	out.Coverage = overviewCoverage(out.LastFull, out.Scanners, executions)
	sort.Slice(out.Scanners, func(i, j int) bool { return out.Scanners[i].ID < out.Scanners[j].ID })
	if store := c.Coordinator.SnapshotStore(); store != nil && baselineID != "" {
		if snapshot, err := store.Get(ctx, baselineID); err == nil {
			out.Baseline = &api.SecurityBaseline{
				SnapshotID: snapshot.ID, CreatedAt: snapshot.CreatedAt, FileCount: snapshot.FileCount,
				UnobservedDirectories: len(snapshot.Unobserved()),
			}
		}
	}
	if out.Baseline != nil {
		since := out.Baseline.CreatedAt
		if out.LastFull != nil && out.LastFull.CompletedAt != nil && out.LastFull.CompletedAt.After(since) {
			since = *out.LastFull.CompletedAt
		}
		introduced, fixed, err := c.Store.FindingChangesSince(ctx, canonical, since)
		if err == nil {
			out.IntroducedSinceBaseline = introduced
			out.FixedSinceBaseline = fixed
		}
	}
	return out, nil
}

// applySeriesReadiness compares covered and current execution identities.
func applySeriesReadiness(state *api.SecurityScannerState, row scanbase.SeriesRow, currentExecution string) {
	state.Watching = true
	if !row.LastCompletedAt.IsZero() {
		at := row.LastCompletedAt
		state.LastCompletedAt = &at
	}
	covered := strings.TrimSpace(row.LastCoveredExecutionFingerprint)
	current := strings.TrimSpace(currentExecution)
	// Supersession requires both execution identities.
	state.PassSuperseded = covered != "" && current != "" && covered != current
}

// currentFullPasses picks, from passes newest first, the newest unfinished
// pass and the newest finished one.
func currentFullPasses(passes []scanbase.FullPass) (running, last *scanbase.FullPass) {
	for i := range passes {
		if passes[i].Finished() {
			if last == nil {
				last = &passes[i]
			}
		} else if running == nil {
			running = &passes[i]
		}
	}
	return running, last
}

// Coverage follows the current scanner selection and execution identities.
func overviewCoverage(last *api.SecurityFullPass, scanners []api.SecurityScannerState, executions map[string]string) api.ScanCoverageStatus {
	if last == nil {
		return api.ScanCoveragePartial
	}
	available := 0
	covered := 0
	for _, scanner := range scanners {
		if !scanner.Available {
			continue
		}
		available++
		for _, member := range last.Members {
			if member.ScannerID != scanner.ID || member.Scan == nil || member.Scan.Status != api.CodeScanStatusComplete {
				continue
			}
			current := executions[scanner.ID]
			if current != "" && member.Scan.ExecutionFingerprint == current {
				covered++
			}
			break
		}
	}
	if available == 0 || last.CoverageStatus == api.ScanCoverageUnavailable {
		return api.ScanCoverageUnavailable
	}
	if covered != len(scanners) {
		return api.ScanCoveragePartial
	}
	return last.CoverageStatus
}

// lastFullAt is when the scanner last completed a full pass.
func lastFullAt(passes []scanbase.FullPass, scannerID string) *time.Time {
	var latest *time.Time
	for _, pass := range passes {
		for _, member := range pass.Members {
			scan := member.Scan
			if member.ScannerID != scannerID || scan == nil || scan.Status != api.CodeScanStatusComplete || scan.CompletedAt == nil {
				continue
			}
			if latest == nil || scan.CompletedAt.After(*latest) {
				at := *scan.CompletedAt
				latest = &at
			}
		}
	}
	return latest
}

func scannerLabel(meta scanbase.ScannerMeta, labels map[string]string) string {
	if label := strings.TrimSpace(labels[meta.ID]); label != "" {
		return label
	}
	if name := strings.TrimSpace(meta.Name); name != "" {
		return name
	}
	return meta.ID
}
