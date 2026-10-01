package cadence

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// Service schedules proactive scans for immutable source generations.
type Service struct {
	Store       *scanbase.SQLStore
	Coordinator scanbase.ScanCoordinator
	Registry    scanbase.CodeScannerRegistry
	Settings    *settings.SecurityScannersStore
	Gates       scancfg.GatesConfig
	Triggers    *scanbase.TriggerService
	Now         func() time.Time
	// Scopes filters writes to admitted paths; nil uses the excludes floor.
	Scopes sourcesnapshot.ScopeProvider
	// OverlayRootsApply selects trusted roots whose scan settings apply; nil disables overlays.
	OverlayRootsApply func(ctx context.Context, rootPaths []string) []string
	// Preempt stops a scan a full pass makes redundant; nil leaves it to finish.
	Preempt func(ctx context.Context, scanID, reason string)

	mu             sync.Mutex
	retiredRoots   map[string]struct{}
	rootDispatches map[string]map[string]context.CancelFunc
}

func New(store *scanbase.SQLStore, coord scanbase.ScanCoordinator, registry scanbase.CodeScannerRegistry, settingsStore *settings.SecurityScannersStore, gates scancfg.GatesConfig, triggers *scanbase.TriggerService) *Service {
	return &Service{Store: store, Coordinator: coord, Registry: registry, Settings: settingsStore, Gates: gates, Triggers: triggers}
}

func (c *Service) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Service) cadenceCfg() scancfg.CadenceConfig {
	if c == nil {
		return scancfg.CadenceConfig{}
	}
	return c.Gates.Gates.Cadence
}

func (c *Service) configured() bool {
	return c != nil && c.Store != nil && c.Coordinator != nil && c.Registry != nil
}

func (c *Service) securityOn() bool {
	return c != nil && (c.Settings == nil || c.Settings.Effective().Enabled)
}

func (c *Service) ObserveRepochange() func() {
	if c == nil {
		return func() {}
	}
	return repochange.RegisterObserver(c.onRepochange)
}

func (c *Service) onRepochange(ctx context.Context, event repochange.Event) {
	if event.Kind != repochange.WorktreeChanged || len(event.Paths) == 0 {
		return
	}
	if err := c.NoteWrites(ctx, event.ProjectDir, event.Paths); err != nil {
		slog.WarnContext(ctx, "scan cadence note writes", "path", event.ProjectDir, "error", err)
	}
}

// BaselineRoot records a baseline for automatic scans of subsequent changes.
func (c *Service) BaselineRoot(ctx context.Context, projectDir string) error {
	if !c.configured() || !c.securityOn() {
		return nil
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return err
	}
	c.mu.Lock()
	series, err := c.syncSeriesLocked(ctx, canonical)
	if err != nil || len(series) == 0 {
		c.mu.Unlock()
		return err
	}
	for i := range series {
		if series[i].LastCoveredSnapshotID != "" || series[i].DispatchToken != "" {
			continue
		}
		if err := c.requestRefreshLocked(ctx, &series[i], 0); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	c.mu.Unlock()
	_, failures := c.dispatchRoot(ctx, canonical)
	if len(failures) > 0 {
		return formatDispatchFailures(failures)
	}
	return nil
}

func (c *Service) NoteWrites(ctx context.Context, projectDir string, paths []string) error {
	if !c.configured() || !c.securityOn() {
		return nil
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return err
	}
	// Filter large path batches before taking the lock.
	changed, wholeTree, err := c.scopedLandedPaths(ctx, canonical, paths)
	if err != nil {
		return err
	}
	if len(changed) == 0 && !wholeTree {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	series, err := c.syncSeriesLocked(ctx, canonical)
	if err != nil {
		return err
	}
	for i := range series {
		row := &series[i]
		if wholeTree {
			row.DesiredPaths = nil
			if err := c.requestRefreshLocked(ctx, row, c.cadenceCfg().RefreshSettle()); err != nil {
				return err
			}
			continue
		}
		if err := c.requestPathsLocked(ctx, row, changed, c.cadenceCfg().WriteBurstSettle()); err != nil {
			return err
		}
	}
	return nil
}

// scopedLandedPaths filters to capture scope; the root path requests a generation refresh.
func (c *Service) scopedLandedPaths(ctx context.Context, canonical string, paths []string) ([]string, bool, error) {
	if c.Scopes == nil {
		changed, err := scanbase.NormalizeLandedPaths(canonical, paths)
		return changed, false, err
	}
	scope := c.Scopes.Capture(ctx, canonical)
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		clean := strings.TrimSpace(filepath.ToSlash(raw))
		if clean == "." || clean == "" || clean == canonical {
			return nil, true, nil
		}
		rel, ok := scanbase.BoundRelUnderRoot(canonical, raw)
		if !ok {
			continue
		}
		if _, exists := seen[rel]; exists {
			continue
		}
		seen[rel] = struct{}{}
		isDir := false
		if info, err := os.Lstat(filepath.Join(canonical, filepath.FromSlash(rel))); err == nil {
			isDir = info.IsDir()
		}
		if !scope.AdmitPath(rel, isDir) {
			continue
		}
		out = append(out, rel)
	}
	return out, false, nil
}

func (c *Service) NoteTree(ctx context.Context, projectDir string, fileCount int) error {
	if !c.configured() || !c.securityOn() {
		return nil
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	series, err := c.syncSeriesLocked(ctx, canonical)
	if err != nil {
		return err
	}
	for i := range series {
		row := &series[i]
		previous := row.LastFileCount
		row.LastFileCount = fileCount
		row.UpdatedAt = c.now()
		if err := c.Store.UpsertSeries(ctx, *row); err != nil {
			return err
		}
		if fileCount <= 0 {
			continue
		}
		// The first file count establishes the drift baseline.
		if previous > 0 && drifted(absInt(fileCount-previous), previous, c.cadenceCfg()) {
			if err := c.requestRefreshLocked(ctx, row, c.cadenceCfg().RefreshSettle()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Service) Tick(ctx context.Context) {
	if !c.configured() || !c.securityOn() {
		return
	}
	for _, root := range c.dueRoots(ctx) {
		c.dispatchRoot(ctx, root)
	}
}

func (c *Service) dueRoots(ctx context.Context) []string {
	due, err := c.Store.ListDueSeries(ctx, c.now())
	if err != nil {
		slog.WarnContext(ctx, "scan cadence list due", "error", err)
		return nil
	}
	roots := make(map[string]struct{}, len(due))
	for _, row := range due {
		roots[row.CanonicalPath] = struct{}{}
	}
	ordered := make([]string, 0, len(roots))
	for root := range roots {
		ordered = append(ordered, root)
	}
	sort.Strings(ordered)
	return ordered
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
