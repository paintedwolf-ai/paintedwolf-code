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

// An unnamed request includes every available selected scanner. Join a covering
// unfinished pass or widen one that has not started; dispatch waits for all members.
func (c *Service) RequestFull(ctx context.Context, projectDir string, scannerIDs []string, trigger api.ScanTrigger, bind scanbase.FullScanContext) (scanbase.FullPass, error) {
	if !c.configured() {
		return scanbase.FullPass{}, fmt.Errorf("scan cadence not configured")
	}
	if !c.securityOn() {
		return scanbase.FullPass{}, scanbase.ErrSecurityScannersOff
	}
	if !fullScanTrigger(trigger) {
		return scanbase.FullPass{}, fmt.Errorf("trigger %q cannot start a full pass", trigger)
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return scanbase.FullPass{}, err
	}
	c.mu.Lock()
	passID, err := c.admitFullPassLocked(ctx, canonical, scannerIDs, trigger)
	if err == nil {
		// Bound before dispatch can start the pass, so its scans carry the binding.
		err = c.Store.BindFullPassRequester(ctx, passID, bind)
	}
	c.mu.Unlock()
	if err != nil {
		return scanbase.FullPass{}, err
	}
	c.preemptPathScans(ctx, canonical, passID)
	c.dispatchRoot(ctx, canonical)
	pass, err := c.Store.FullPass(ctx, passID)
	if err != nil {
		return scanbase.FullPass{}, err
	}
	if pass == nil {
		return scanbase.FullPass{}, fmt.Errorf("full pass %s was not recorded", passID)
	}
	return *pass, nil
}

func (c *Service) admitFullPassLocked(ctx context.Context, canonical string, scannerIDs []string, trigger api.ScanTrigger) (string, error) {
	series, err := c.syncSeriesLocked(ctx, canonical)
	if err != nil {
		return "", err
	}
	members, err := c.fullPassMembers(ctx, canonical, series, scannerIDs)
	if err != nil {
		return "", err
	}
	passes, err := c.Store.FullPassesForPath(ctx, canonical)
	if err != nil {
		return "", err
	}
	if open := newestUnfinishedPass(passes); open != nil {
		if !passStarting(*open, series) {
			widened, err := c.Store.WidenFullPass(ctx, open.ID, unionSorted(open.ScannerIDs(), members))
			if err != nil {
				return "", err
			}
			if widened {
				return open.ID, c.oweFullPassLocked(ctx, series, members, open.ID, trigger)
			}
		}
		if open.Covers(members) {
			return open.ID, nil
		}
	}
	id := uuid.NewString()
	if err := c.Store.InsertFullPass(ctx, scanbase.FullPassDraft{
		ID: id, CanonicalPath: canonical, Scanners: members, Trigger: trigger, RequestedAt: c.now(),
	}); err != nil {
		return "", err
	}
	return id, c.oweFullPassLocked(ctx, series, members, id, trigger)
}

// fullPassMembers resolves the scanners a request names. A named scanner must
// be selected and able to run; an unnamed request takes every one that can.
func (c *Service) fullPassMembers(ctx context.Context, canonical string, series []scanbase.SeriesRow, scannerIDs []string) ([]string, error) {
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
			if _, err := c.resolveExecution(ctx, canonical, row); err != nil {
				return nil, &scancatalog.ErrInvalidScanEngineSelection{ScannerID: id, Reason: err.Error()}
			}
		}
		return named, nil
	}
	members := make([]string, 0, len(series))
	for _, row := range series {
		if _, err := c.resolveExecution(ctx, canonical, row); err == nil {
			members = append(members, row.ScannerID)
		}
	}
	if len(members) == 0 {
		return nil, scanbase.ErrNoScannerAvailable
	}
	return scanbase.UniqueSortedStrings(members), nil
}

func (c *Service) oweFullPassLocked(ctx context.Context, series []scanbase.SeriesRow, members []string, passID string, trigger api.ScanTrigger) error {
	bySeries := make(map[string]scanbase.SeriesRow, len(series))
	for _, row := range series {
		bySeries[row.ScannerID] = row
	}
	for _, id := range members {
		row := bySeries[id]
		if err := c.requestFullLocked(ctx, &row, passID, trigger); err != nil {
			return err
		}
	}
	return nil
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

// preemptPathScans stops the cadence's own path-scoped rescans that a pass's
// scanners still have open, since pass members start together and the pass
// covers their work. Scans another consumer asked for always finish.
func (c *Service) preemptPathScans(ctx context.Context, canonical, passID string) {
	if c.Preempt == nil {
		return
	}
	pass, err := c.Store.FullPass(ctx, passID)
	if err != nil || pass == nil || !pass.StartedAt.IsZero() {
		return
	}
	members := make(map[string]bool, len(pass.Members))
	for _, m := range pass.Members {
		members[m.ScannerID] = true
	}
	open, err := c.Store.OpenScansForPath(ctx, canonical)
	if err != nil {
		slog.WarnContext(ctx, "list open scans for preemption", "path", canonical, "error", err)
		return
	}
	for _, s := range open {
		if members[s.ScannerID] && s.TargetKind == api.ScanTargetPaths && proactiveTrigger(s.Trigger) {
			c.Preempt(ctx, s.ID, "superseded by a full pass")
		}
	}
}
