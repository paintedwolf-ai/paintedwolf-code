package cadence

import (
	"context"
	"log/slog"
	"strings"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxDispatchBackoffCap = 2 * time.Minute

func dispatchBackoff(cfg scancfg.CadenceConfig, consecutiveFailures int) time.Duration {
	base := cfg.WriteBurstSettle()
	if base <= 0 {
		base = time.Second
	}
	capDelay := min(maxDispatchBackoffCap, cfg.MaxDefer()/4)
	if capDelay < base {
		capDelay = max(cfg.MaxDefer()/2, base)
	}
	if consecutiveFailures <= 1 {
		return base
	}
	shift := min(consecutiveFailures-1, 10)
	delay := base << shift
	if delay <= 0 || delay > capDelay {
		return capDelay
	}
	return delay
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func currentScannerFaultIdentity(ctx context.Context, registry scanbase.CodeScannerRegistry, canonical, scannerID string, categories ...api.ScanCategory) string {
	if registry == nil {
		return ""
	}
	contract, err := scanbase.SelectedScannerContract(ctx, registry, canonical, scannerID, categories...)
	if err == nil {
		if _, fingerprint, manifestErr := scancatalog.ExecutionManifest(contract); manifestErr == nil {
			return "manifest:" + fingerprint
		}
		return "contract:" + contract.IdentityDigest()
	}
	for _, meta := range scanbase.ListSelectedScanners(ctx, registry, canonical, categories...) {
		if meta.ID == scannerID {
			return "contract:" + meta.Contract.IdentityDigest()
		}
	}
	return "unselected:" + strings.TrimSpace(scannerID)
}

func scannerFaultCleared(row scanbase.SeriesRow, currentIdentity string) bool {
	if !row.Unavailable() {
		return false
	}
	if row.UnavailableContractIdentity == "" {
		return strings.HasPrefix(currentIdentity, "manifest:")
	}
	return currentIdentity != row.UnavailableContractIdentity
}

func markUnavailableDispatch(ctx context.Context, c *Service, claimed scanbase.SeriesRow, fault *scancatalog.ConfigFaultError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, err := c.Store.GetSeries(ctx, claimed.CanonicalPath, claimed.ScannerID)
	if err != nil {
		slog.WarnContext(ctx, "read scan dispatch for unavailable mark", "scanner_id", claimed.ScannerID, "error", err)
		return
	}
	if row == nil || row.DispatchToken != claimed.DispatchToken {
		return
	}
	if claimed.DispatchPassID == "" {
		holdDispatchedDesire(row)
	}
	row.DesiredPassID = ""
	releaseClaim(row)
	row.DirtySinceAt = time.Time{}
	row.DueAt = time.Time{}
	row.MaxDueAt = time.Time{}
	row.ConsecutiveFailures = 0
	row.UnavailableReason = fault.Reason
	row.UnavailableDetail = fault.Error()
	row.UnavailableContractIdentity = currentScannerFaultIdentity(ctx, c.Registry, row.CanonicalPath, row.ScannerID, row.Categories...)
	row.UpdatedAt = nowUTC(c)
	if err := c.Store.UpsertSeries(ctx, *row); err != nil {
		slog.WarnContext(ctx, "mark scan series unavailable", "scanner_id", row.ScannerID, "error", err)
	}
}

func refreshAvailabilityLocked(ctx context.Context, c *Service, row *scanbase.SeriesRow) (bool, error) {
	if row == nil || !row.Unavailable() {
		return false, nil
	}
	currentIdentity := currentScannerFaultIdentity(ctx, c.Registry, row.CanonicalPath, row.ScannerID, row.Categories...)
	if !scannerFaultCleared(*row, currentIdentity) {
		return false, nil
	}
	now := nowUTC(c)
	row.ClearUnavailable()
	row.ConsecutiveFailures = 0
	if !row.WantsDispatch() && row.LastCoveredSnapshotID == "" {
		row.DesiredTrigger = api.ScanTriggerAuthorityRefresh
	}
	if row.WantsDispatch() {
		(seriesPlanner{c: c}).arm(row, now, 0)
	}
	row.UpdatedAt = now
	if err := c.Store.UpsertSeries(ctx, *row); err != nil {
		return false, err
	}
	return true, nil
}

func reconcileUnavailable(ctx context.Context, c *Service) {
	if !configured(c) {
		return
	}
	paths := scanbase.UnavailableSeriesPaths(c.Store)
	for _, canonical := range paths {
		c.mu.Lock()
		if c.roots.isRetired(canonical) {
			c.mu.Unlock()
			continue
		}
		rows, err := c.Store.ListSeriesForPath(ctx, canonical)
		if err != nil {
			c.mu.Unlock()
			continue
		}
		for i := range rows {
			if _, err := refreshAvailabilityLocked(ctx, c, &rows[i]); err != nil {
				slog.WarnContext(ctx, "refresh scan series availability", "scanner_id", rows[i].ScannerID, "error", err)
			}
		}
		c.mu.Unlock()
	}
}
