package cadence

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

type fullPassAdmission struct {
	c *Service
}

// RequestFull joins a covering pass or widens an unstarted one; unnamed requests select all available scanners.
func (c *Service) RequestFull(ctx context.Context, projectDir string, scannerIDs []string, trigger api.ScanTrigger, bind scanbase.FullScanContext) (scanbase.FullPass, error) {
	if !configured(c) {
		return scanbase.FullPass{}, fmt.Errorf("scan cadence not configured")
	}
	if !securityOn(c) {
		return scanbase.FullPass{}, scanbase.ErrSecurityScannersOff
	}
	if !fullScanTrigger(trigger) {
		return scanbase.FullPass{}, fmt.Errorf("trigger %q cannot start a full pass", trigger)
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return scanbase.FullPass{}, err
	}
	admission := fullPassAdmission{c: c}
	c.mu.Lock()
	passID, err := admission.admitLocked(ctx, canonical, scannerIDs, trigger)
	if err == nil {
		// Bound before dispatch can start the pass, so its scans carry the binding.
		err = c.Store.BindFullPassRequester(ctx, passID, bind)
	}
	c.mu.Unlock()
	if err != nil {
		return scanbase.FullPass{}, err
	}
	admission.preemptPathScans(ctx, canonical, passID)
	(rootDispatcher{c: c}).dispatchRoot(ctx, canonical)
	pass, err := c.Store.FullPass(ctx, passID)
	if err != nil {
		return scanbase.FullPass{}, err
	}
	if pass == nil {
		return scanbase.FullPass{}, fmt.Errorf("full pass %s was not recorded", passID)
	}
	return *pass, nil
}

func (f fullPassAdmission) admitLocked(ctx context.Context, canonical string, scannerIDs []string, trigger api.ScanTrigger) (string, error) {
	series, err := seriesPlanner(f).syncLocked(ctx, canonical)
	if err != nil {
		return "", err
	}
	members, err := f.members(ctx, canonical, series, scannerIDs)
	if err != nil {
		return "", err
	}
	passes, err := f.c.Store.FullPassesForPath(ctx, canonical)
	if err != nil {
		return "", err
	}
	if open := newestUnfinishedPass(passes); open != nil {
		if !passStarting(*open, series) {
			widened, err := f.c.Store.WidenFullPass(ctx, open.ID, unionSorted(open.ScannerIDs(), members))
			if err != nil {
				return "", err
			}
			if widened {
				return open.ID, f.oweLocked(ctx, series, members, open.ID, trigger)
			}
		}
		if open.Covers(members) {
			return open.ID, nil
		}
	}
	id := uuid.NewString()
	if err := f.c.Store.InsertFullPass(ctx, scanbase.FullPassDraft{
		ID: id, CanonicalPath: canonical, Scanners: members, Trigger: trigger, RequestedAt: nowUTC(f.c),
	}); err != nil {
		return "", err
	}
	return id, f.oweLocked(ctx, series, members, id, trigger)
}

// members validates named scanners or selects all runnable scanners when unnamed.
func (f fullPassAdmission) members(ctx context.Context, canonical string, series []scanbase.SeriesRow, scannerIDs []string) ([]string, error) {
	bySeries := make(map[string]scanbase.SeriesRow, len(series))
	for _, row := range series {
		bySeries[row.ScannerID] = row
	}
	named := scanbase.UniqueSortedStrings(scannerIDs)
	if len(named) > 0 {
		for _, id := range named {
			row, selected := bySeries[id]
			if !selected {
				return nil, &scancatalog.ErrInvalidScanEngineSelection{ScannerID: id, Reason: "not selected for this project"}
			}
			if _, err := f.resolveExecution(ctx, canonical, row); err != nil {
				return nil, &scancatalog.ErrInvalidScanEngineSelection{ScannerID: id, Reason: err.Error()}
			}
		}
		return named, nil
	}
	members := make([]string, 0, len(series))
	for _, row := range series {
		if _, err := f.resolveExecution(ctx, canonical, row); err == nil {
			members = append(members, row.ScannerID)
		}
	}
	if len(members) == 0 {
		return nil, scanbase.ErrNoScannerAvailable
	}
	return scanbase.UniqueSortedStrings(members), nil
}

func (f fullPassAdmission) oweLocked(ctx context.Context, series []scanbase.SeriesRow, members []string, passID string, trigger api.ScanTrigger) error {
	bySeries := make(map[string]scanbase.SeriesRow, len(series))
	for _, row := range series {
		bySeries[row.ScannerID] = row
	}
	planner := seriesPlanner(f)
	for _, id := range members {
		row := bySeries[id]
		if err := planner.requestFullLocked(ctx, &row, passID, trigger); err != nil {
			return err
		}
	}
	return nil
}

func (f fullPassAdmission) resolveExecution(ctx context.Context, canonical string, row scanbase.SeriesRow) (dispatchExecution, error) {
	contract, err := scanbase.SelectedScannerContract(ctx, f.c.Registry, canonical, row.ScannerID, row.Categories...)
	if err != nil {
		return dispatchExecution{}, err
	}
	manifest, fingerprint, err := scancatalog.ExecutionManifest(contract)
	if err != nil {
		return dispatchExecution{}, err
	}
	return dispatchExecution{Manifest: manifest, Fingerprint: fingerprint}, nil
}

func newestUnfinishedPass(passes []scanbase.FullPass) *scanbase.FullPass {
	for i := range passes {
		if !passes[i].Finished() {
			return &passes[i]
		}
	}
	return nil
}

// An in-flight dispatch fixes the pass's generation before its members start.
func passStarting(pass scanbase.FullPass, series []scanbase.SeriesRow) bool {
	if pass.Started() {
		return true
	}
	for _, row := range series {
		if row.DispatchPassID == pass.ID {
			return true
		}
	}
	return false
}

func fullScanTrigger(trigger api.ScanTrigger) bool {
	switch trigger {
	case api.ScanTriggerManual, api.ScanTriggerScanPack, api.ScanTriggerPhaseEnter:
		return true
	case api.ScanTriggerLandedChange, api.ScanTriggerWriteBurst, api.ScanTriggerAuthorityRefresh:
	}
	return false
}

// preemptPathScans stops proactive path-scoped scans superseded by the pass.
func (f fullPassAdmission) preemptPathScans(ctx context.Context, canonical, passID string) {
	if f.c.Preempt == nil {
		return
	}
	pass, err := f.c.Store.FullPass(ctx, passID)
	if err != nil || pass == nil || !pass.StartedAt.IsZero() {
		return
	}
	members := make(map[string]bool, len(pass.Members))
	for _, m := range pass.Members {
		members[m.ScannerID] = true
	}
	open, err := f.c.Store.OpenScansForPath(ctx, canonical)
	if err != nil {
		slog.WarnContext(ctx, "list open scans for preemption", "path", canonical, "error", err)
		return
	}
	for _, s := range open {
		if members[s.ScannerID] && s.TargetKind == api.ScanTargetPaths && proactiveTrigger(s.Trigger) {
			f.c.Preempt(ctx, s.ID, "superseded by a full pass")
		}
	}
}
