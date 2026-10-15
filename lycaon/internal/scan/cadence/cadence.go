package cadence

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// Service schedules proactive scans for immutable source generations.
type Service struct {
	IgnoreService
	Coordinator scanbase.ScanCoordinator
	Registry    scanbase.CodeScannerRegistry
	Settings    *settings.SecurityScannersStore
	Gates       scancfg.GatesConfig
	Triggers    *scanbase.TriggerService
	Now         func() time.Time
	// Scopes filters writes to admitted paths; nil uses the excludes floor.
	Scopes sourcesnapshot.ScopeProvider
	// Preempt stops a scan a full pass makes redundant; nil leaves it to finish.
	Preempt func(ctx context.Context, scanID, reason string)

	mu    sync.Mutex
	roots rootTracker
}

func New(store *scanbase.SQLStore, coord scanbase.ScanCoordinator, registry scanbase.CodeScannerRegistry, settingsStore *settings.SecurityScannersStore, gates scancfg.GatesConfig, triggers *scanbase.TriggerService) *Service {
	return &Service{
		IgnoreService: IgnoreService{Store: store},
		Coordinator:   coord,
		Registry:      registry,
		Settings:      settingsStore,
		Gates:         gates,
		Triggers:      triggers,
	}
}

func nowUTC(c *Service) time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func cadenceCfg(c *Service) scancfg.CadenceConfig {
	if c == nil {
		return scancfg.CadenceConfig{}
	}
	return c.Gates.Gates.Cadence
}

func configured(c *Service) bool {
	return c != nil && c.Store != nil && c.Coordinator != nil && c.Registry != nil
}

func securityOn(c *Service) bool {
	return c != nil && (c.Settings == nil || c.Settings.Effective().Enabled)
}

// BaselineRoot records a baseline for automatic scans of subsequent changes.
func (c *Service) BaselineRoot(ctx context.Context, projectDir string) error {
	if !configured(c) || !securityOn(c) {
		return nil
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return err
	}
	planner := seriesPlanner{c: c}
	c.mu.Lock()
	series, err := planner.syncLocked(ctx, canonical)
	if err != nil || len(series) == 0 {
		c.mu.Unlock()
		return err
	}
	for i := range series {
		if series[i].LastCoveredSnapshotID != "" || series[i].DispatchToken != "" {
			continue
		}
		if err := planner.requestRefreshLocked(ctx, &series[i], 0); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	c.mu.Unlock()
	_, failures := (rootDispatcher{c: c}).dispatchRoot(ctx, canonical)
	if len(failures) > 0 {
		return formatDispatchFailures(failures)
	}
	return nil
}

func (c *Service) NoteWrites(ctx context.Context, projectDir string, paths []string) error {
	if !configured(c) || !securityOn(c) {
		return nil
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return err
	}
	planner := seriesPlanner{c: c}
	// Filter large path batches before taking the lock.
	changed, wholeTree, err := planner.scopedLandedPaths(ctx, canonical, paths)
	if err != nil {
		return err
	}
	if len(changed) == 0 && !wholeTree {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	series, err := planner.syncLocked(ctx, canonical)
	if err != nil {
		return err
	}
	cfg := cadenceCfg(c)
	for i := range series {
		row := &series[i]
		if wholeTree {
			row.DesiredPaths = nil
			if err := planner.requestRefreshLocked(ctx, row, cfg.RefreshSettle()); err != nil {
				return err
			}
			continue
		}
		if err := planner.requestPathsLocked(ctx, row, changed, cfg.WriteBurstSettle()); err != nil {
			return err
		}
	}
	return nil
}

func (c *Service) NoteTree(ctx context.Context, projectDir string, fileCount int) error {
	if !configured(c) || !securityOn(c) {
		return nil
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return err
	}
	planner := seriesPlanner{c: c}
	c.mu.Lock()
	defer c.mu.Unlock()
	series, err := planner.syncLocked(ctx, canonical)
	if err != nil {
		return err
	}
	cfg := cadenceCfg(c)
	for i := range series {
		row := &series[i]
		previous := row.LastFileCount
		row.LastFileCount = fileCount
		row.UpdatedAt = nowUTC(c)
		if err := c.Store.UpsertSeries(ctx, *row); err != nil {
			return err
		}
		if fileCount <= 0 {
			continue
		}
		// The first file count establishes the drift baseline.
		if previous > 0 && drifted(absInt(fileCount-previous), previous, cfg) {
			if err := planner.requestRefreshLocked(ctx, row, cfg.RefreshSettle()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Service) Tick(ctx context.Context) {
	if !configured(c) || !securityOn(c) {
		return
	}
	dispatcher := rootDispatcher{c: c}
	for _, root := range dispatcher.dueRoots(ctx) {
		dispatcher.dispatchRoot(ctx, root)
	}
}

func drifted(delta, baseline int, cfg scancfg.CadenceConfig) bool {
	if delta <= 0 {
		return false
	}
	minPaths := cfg.DriftMinPaths
	if minPaths <= 0 {
		minPaths = scancfg.DefaultDriftMinPaths
	}
	ratio := cfg.DriftRatio
	if ratio <= 0 {
		ratio = scancfg.DefaultDriftRatio
	}
	return delta >= minPaths && (baseline <= 0 || float64(delta)/float64(baseline) >= ratio)
}

func unionSorted(existing, added []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(added))
	out := make([]string, 0, len(existing)+len(added))
	for _, path := range append(append([]string(nil), existing...), added...) {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func subtractSorted(existing, removed []string) []string {
	drop := make(map[string]struct{}, len(removed))
	for _, path := range removed {
		drop[strings.TrimSpace(path)] = struct{}{}
	}
	out := make([]string, 0, len(existing))
	for _, path := range existing {
		if _, found := drop[path]; !found {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

func burstIsWholeTree(paths []string, fileCount int, cfg scancfg.CadenceConfig) bool {
	return drifted(len(paths), fileCount, cfg)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
