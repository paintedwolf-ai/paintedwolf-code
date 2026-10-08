// Package repoinfo builds compact repository orientation summaries.
package repoinfo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/pkg/api"
)

var briefLog = observability.LazyComponent("repoinfo")

const (
	// MaxLanguages caps the dominant languages reported in Brief.Languages.
	MaxLanguages = 5
	// MaxClassifyBytes caps per-file classification input.
	MaxClassifyBytes = 16 * 1024
	// briefRevalidateAfter spaces measurements of newly settled generations.
	briefRevalidateAfter = time.Minute
	// briefIndexJoin bounds waiting for a cold root's first generation.
	briefIndexJoin = 2 * time.Second
	// Status checks avoid rescanning indexed files.
	briefDiscoveryPoll = 2 * time.Second
	// briefRefineCeiling caps the delay between partial measurements.
	briefRefineCeiling = 30 * time.Second
	// Continuous writes can prevent discovery from settling.
	briefDiscoveryLimit = 10 * time.Minute
)

// Brief describes repository shape at one file-index generation.
type Brief struct {
	Languages    []string       `json:"languages"`
	FileCount    int            `json:"file_count"`
	Layout       api.RepoLayout `json:"layout,omitempty"`
	GeneratedAt  time.Time      `json:"generated_at"`
	Materialized bool           `json:"-"`
	Root         string         `json:"-"`
	// Generation is the file-index revision this brief was computed from.
	Generation uint64 `json:"-"`
	// Partial includes pending discovery, refreshes, and recorded omissions.
	Partial             bool `json:"-"`
	Refreshing          bool `json:"-"`
	measurementDuration time.Duration
	omissions           bool
}

// Current reports whether the brief describes a whole tree.
func Current(b *Brief) bool {
	return b != nil && b.Materialized && !b.Partial && !b.Refreshing
}

// Provider returns a Brief for projectDir, caching by directory.
type Provider interface {
	Brief(ctx context.Context, projectDir string) (*Brief, error)
	// KnownEmpty requires an exhaustive measurement.
	KnownEmpty(ctx context.Context, projectDir string) (bool, error)
	Warm(projectDir string)
	// Changed applies measurement pacing to filesystem changes.
	Changed(ctx context.Context, projectDir string)
	// SetOnSettled notifies readers after each materialized measurement.
	SetOnSettled(fn func(projectDir string))
	// Close stops background analysis.
	Close() error
}

// CatalogRoot identifies the shared source-catalog generation for a project root.
type CatalogRoot struct {
	ProjectID string
	RootID    string
}

// CatalogRootResolver maps a root path to its project/catalog identity.
type CatalogRootResolver func(ctx context.Context, projectDir string) (CatalogRoot, bool, error)

type repositoryIndex interface {
	OpenIndex(context.Context, string, sourcecatalog.Root, time.Duration) (*sourcecatalog.IndexReader, sourcecatalog.TreeStatus, error)
	IndexStatus(context.Context, string, sourcecatalog.Root) (sourcecatalog.TreeStatus, error)
	RootFileCount(context.Context, string, sourcecatalog.Root, sourcecatalog.FileScope, time.Duration) (sourcecatalog.RootFiles, error)
}

// NewProvider uses the supplied catalog and root identities.
func NewProvider(catalog *sourcecatalog.Catalog, resolve CatalogRootResolver, cacheDir string) Provider {
	if catalog == nil || resolve == nil {
		panic("repo provider requires a catalog and root resolver")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &fileProvider{
		cache: make(map[string]*Brief), catalog: catalog, resolveCatalogRoot: resolve,
		memo: newLanguageMemo(), ctx: ctx, cancel: cancel, cacheDir: cacheDir,
	}
}

// AwaitBrief accepts settled summaries even when coverage has omissions.
func AwaitBrief(ctx context.Context, p Provider, projectDir string) (*Brief, error) {
	p.Warm(projectDir)
	for {
		brief, err := p.Brief(ctx, projectDir)
		if err != nil {
			return nil, err
		}
		if brief != nil && brief.Materialized && !brief.Refreshing {
			return brief, nil
		}
		select {
		case <-ctx.Done():
			return brief, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type fileProvider struct {
	mu       sync.RWMutex
	cache    map[string]*Brief
	warming  map[string]struct{}
	closed   bool
	cacheDir string

	// analyzeFn supplies analysis in tests.
	analyzeFn          func(ctx context.Context, projectDir string) (*Brief, error)
	catalog            repositoryIndex
	resolveCatalogRoot CatalogRootResolver
	// memo carries content classifications between analyses of one root.
	memo *languageMemo
	// onSettled fires after a materialized brief is stored.
	onSettled func(projectDir string)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *fileProvider) Brief(ctx context.Context, projectDir string) (*Brief, error) {
	key, err := cacheKey(projectDir)
	if err != nil {
		return nil, err
	}
	cached := p.load(key)
	if cached == nil {
		// Persisted summaries remain visible during remeasurement.
		cached = p.cachedBrief(key)
		if cached != nil {
			p.store(key, cached)
		}
	}
	files, coverageErr := p.indexGeneration(ctx, key)
	if cached == nil || !cached.Materialized || coverageErr == nil && briefStale(cached, files, time.Now()) {
		p.ensureAnalyze(key)
	}
	if cached == nil {
		// Unmeasured summaries omit counts.
		return emptyBrief(), nil
	}
	briefLog.Debug("repo brief read",
		"project_dir", key,
		"file_count", cached.FileCount,
		"generation", cached.Generation,
		"partial", cached.Partial,
	)
	shown := cloneBrief(cached)
	if coverageErr != nil || files.failed {
		shown.Partial, shown.Refreshing = true, false
	} else if files.pending {
		shown.Partial, shown.Refreshing = true, true
	}
	return shown, nil
}

// stale applies measurement pacing to the current index.
func (p *fileProvider) stale(ctx context.Context, projectDir string, brief *Brief) bool {
	if brief == nil || !brief.Materialized {
		return true
	}
	files, err := p.indexGeneration(ctx, projectDir)
	if err != nil {
		return false
	}
	return briefStale(brief, files, time.Now())
}

// briefStale schedules settled generations and completed refreshes.
func briefStale(brief *Brief, files indexGeneration, now time.Time) bool {
	switch {
	case brief == nil || !brief.Materialized:
		return true
	case now.Sub(brief.GeneratedAt) < max(briefRevalidateAfter, 10*brief.measurementDuration):
		return false
	case brief.Refreshing && files.complete:
		// Settling can change coverage without publishing another generation.
		return true
	case files.generation == 0 || brief.Generation == files.generation:
		return false
	case !files.complete:
		// The analysis loop owns refinement during discovery.
		return false
	case brief.Partial:
		return true
	default:
		return now.Sub(brief.GeneratedAt) >= briefRevalidateAfter
	}
}

func (p *fileProvider) Changed(ctx context.Context, projectDir string) {
	key, err := cacheKey(projectDir)
	if err != nil {
		return
	}
	// Settled measurements remain available during reanalysis.
	if p.stale(ctx, key, p.load(key)) {
		p.ensureAnalyze(key)
	}
}

func (p *fileProvider) SetOnSettled(fn func(projectDir string)) {
	p.mu.Lock()
	p.onSettled = fn
	p.mu.Unlock()
}

func (p *fileProvider) Warm(projectDir string) {
	key, err := cacheKey(projectDir)
	if err != nil {
		return
	}
	if cached := p.load(key); Current(cached) {
		return
	} else if cached == nil {
		if persisted := p.cachedBrief(key); persisted != nil {
			p.store(key, persisted)
		}
	}
	p.ensureAnalyze(key)
}

// ensureAnalyze starts one background analysis per root.
func (p *fileProvider) ensureAnalyze(projectDir string) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if brief := p.cache[projectDir]; brief != nil && brief.Materialized && time.Since(brief.GeneratedAt) < max(briefRevalidateAfter, 10*brief.measurementDuration) {
		p.mu.Unlock()
		return
	}
	if p.warming == nil {
		p.warming = make(map[string]struct{})
	}
	if _, ok := p.warming[projectDir]; ok {
		p.mu.Unlock()
		return
	}
	p.warming[projectDir] = struct{}{}
	p.wg.Add(1)
	p.mu.Unlock()

	go func() {
		defer p.wg.Done()
		defer func() {
			p.mu.Lock()
			delete(p.warming, projectDir)
			p.mu.Unlock()
		}()
		p.loadClassifications(projectDir)
		// Background refinement ends when discovery settles.
		deadline := time.Now().Add(briefDiscoveryLimit)
		delay := briefDiscoveryPoll
		for {
			start := time.Now()
			brief, err := p.analyze(p.ctx, projectDir)
			if err != nil {
				observability.LogLatency("repoinfo", "repo brief analyze failed", start,
					"project_dir", projectDir, "err", err)
				return
			}
			if brief == nil {
				if time.Now().After(deadline) || !p.waitDiscovery() {
					return
				}
				continue
			}
			brief.measurementDuration = time.Since(start)
			brief.GeneratedAt = time.Now().UTC()
			brief.Materialized = true
			brief.Root = projectDir
			p.store(projectDir, brief)
			p.persistOrientation(projectDir, brief)
			observability.LogLatency("repoinfo", "repo brief analyzed", start,
				"project_dir", projectDir,
				"file_count", brief.FileCount,
				"generation", brief.Generation,
				"partial", brief.Partial,
			)
			p.notifySettled(projectDir)
			// A brief with no generation behind it has nothing to converge on.
			if !brief.Refreshing || brief.Generation == 0 {
				return
			}
			// Status polling avoids repeated full-index measurements.
			next, refine := p.awaitRefinement(projectDir, brief, delay, deadline)
			if !refine {
				return
			}
			delay = next
		}
	}()
}

// awaitRefinement observes settlement without bypassing measurement pacing.
func (p *fileProvider) awaitRefinement(projectDir string, brief *Brief, delay time.Duration, deadline time.Time) (time.Duration, bool) {
	waited := time.Duration(0)
	for {
		if !p.waitDiscovery() || time.Now().After(deadline) {
			return delay, false
		}
		waited += briefDiscoveryPoll
		files, err := p.indexGeneration(p.ctx, projectDir)
		if err != nil {
			return delay, false
		}

		if files.complete && files.generation == brief.Generation {
			settled := cloneBrief(brief)
			settled.Refreshing, settled.Partial = false, brief.omissions
			p.store(projectDir, settled)
			p.notifySettled(projectDir)
			return delay, false
		}
		if time.Since(brief.GeneratedAt) < max(briefRevalidateAfter, 10*brief.measurementDuration) {
			continue
		}
		if files.complete || files.failed {
			return briefDiscoveryPoll, true
		}

		if waited >= delay && files.generation != brief.Generation {
			return min(2*delay, briefRefineCeiling), true
		}
	}
}

// waitDiscovery stops polling when the provider closes.
func (p *fileProvider) waitDiscovery() bool {
	select {
	case <-p.ctx.Done():
		return false
	case <-time.After(briefDiscoveryPoll):
		return true
	}
}

func (p *fileProvider) notifySettled(projectDir string) {
	p.mu.RLock()
	fn := p.onSettled
	p.mu.RUnlock()
	if fn != nil {
		fn(projectDir)
	}
}

// catalogRoot resolves a path to the identity its file index is keyed by.
func (p *fileProvider) catalogRoot(ctx context.Context, projectDir string) sourcecatalog.Root {
	identity, ok, err := p.resolveCatalogRoot(ctx, projectDir)
	if err != nil || !ok {
		// Unregistered workspaces use their absolute path as identity.
		identity = CatalogRoot{ProjectID: projectDir, RootID: projectDir}
	}
	return sourcecatalog.Root{ID: identity.RootID, Path: projectDir}
}

func (p *fileProvider) catalogProject(ctx context.Context, projectDir string) string {
	identity, ok, err := p.resolveCatalogRoot(ctx, projectDir)
	if err != nil || !ok {
		return projectDir
	}
	return identity.ProjectID
}

// A nil brief without an error means discovery has not published a generation.
func (p *fileProvider) analyze(ctx context.Context, projectDir string) (*Brief, error) {
	if p.analyzeFn != nil {
		return p.analyzeFn(ctx, projectDir)
	}
	reader, status, err := p.catalog.OpenIndex(ctx, p.catalogProject(ctx, projectDir),
		p.catalogRoot(ctx, projectDir), briefIndexJoin)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		if status.State == sourcecatalog.StateFailed {
			return nil, fmt.Errorf("source index: %s", status.Error)
		}
		return nil, nil
	}
	defer func() { _ = reader.Close() }()
	analysis := catalogAnalysis{projectDir: projectDir, memo: p.memo, sniff: readHead}
	brief, err := analysis.run(ctx, reader)
	if err != nil {
		return nil, err
	}
	coverage, err := reader.Coverage(ctx)
	if err != nil {
		return nil, err
	}
	brief.omissions = coverage.BoundedDirectories > 0 || coverage.FailedDirectories > 0 || coverage.Error != ""
	brief.Generation = reader.Status.Revision
	brief.Partial, brief.Refreshing = !coverage.Exhaustive(), coverage.Pending()
	return brief, nil
}

type indexGeneration struct {
	generation uint64
	complete   bool
	failed     bool
	pending    bool
}

// indexGeneration reads publication status without waiting for discovery.
func (p *fileProvider) indexGeneration(ctx context.Context, projectDir string) (indexGeneration, error) {
	if p.catalog == nil {
		return indexGeneration{}, nil
	}
	status, err := p.catalog.IndexStatus(ctx, p.catalogProject(ctx, projectDir), p.catalogRoot(ctx, projectDir))
	if err != nil {
		return indexGeneration{}, err
	}
	return indexGeneration{generation: status.Revision,
		complete: status.Complete && !status.Refreshing && status.Error == "", failed: status.Error != "",
		pending: status.Error == "" && (!status.Complete || status.Refreshing)}, nil
}

// KnownEmpty reads a current generation without waiting for discovery. Cold,
// incomplete, and refreshing roots remain unknown while indexing continues.
func (p *fileProvider) KnownEmpty(ctx context.Context, projectDir string) (bool, error) {
	key, err := cacheKey(projectDir)
	if err != nil {
		return false, err
	}
	if p.catalog == nil {
		return false, nil
	}
	files, err := p.catalog.RootFileCount(ctx, p.catalogProject(ctx, key), p.catalogRoot(ctx, key), sourcecatalog.FileScope{Audience: sourcecatalog.AgentAudience, IncludeHidden: true}, 0)
	if err != nil {
		return false, err
	}
	return files.Measured && files.Count == 0, nil
}

// Close cancels analysis and waits for workers.
func (p *fileProvider) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	p.cancel()
	p.wg.Wait()
	p.memo.forget()
	return nil
}

func emptyBrief() *Brief {
	return &Brief{GeneratedAt: time.Now().UTC()}
}

func cacheKey(projectDir string) (string, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return "", errors.New("project_dir required")
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func (p *fileProvider) load(projectDir string) *Brief {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cache[projectDir]
}

func (p *fileProvider) store(projectDir string, b *Brief) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = make(map[string]*Brief)
	}
	p.cache[projectDir] = cloneBrief(b)
}

func cloneBrief(b *Brief) *Brief {
	if b == nil {
		return nil
	}
	out := *b
	if len(b.Languages) > 0 {
		out.Languages = append([]string(nil), b.Languages...)
	}
	if len(b.Layout.Files) > 0 {
		out.Layout.Files = append([]string(nil), b.Layout.Files...)
	}
	if len(b.Layout.TopLevel) > 0 {
		out.Layout.TopLevel = append([]string(nil), b.Layout.TopLevel...)
	}
	return &out
}

func dominantLanguages(bytesByLang map[string]int64) []string {
	type entry struct {
		lang  string
		bytes int64
	}
	rows := make([]entry, 0, len(bytesByLang))
	for lang, n := range bytesByLang {
		rows = append(rows, entry{lang: lang, bytes: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].bytes != rows[j].bytes {
			return rows[i].bytes > rows[j].bytes
		}
		return rows[i].lang < rows[j].lang
	})
	if len(rows) > MaxLanguages {
		rows = rows[:MaxLanguages]
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.lang)
	}
	return out
}
