package registry

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	bundleddriver "github.com/lycaon/lycaon/internal/scan/drivers/bundled"
	"github.com/lycaon/lycaon/internal/scan/drivers/external"
	"github.com/lycaon/lycaon/internal/scan/drivers/libraryworker"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/workscope"
	"github.com/lycaon/lycaon/pkg/api"
)

// Options configures CodeScannerRegistry construction.
type Options struct {
	ScannerFingerprintKey []byte
	// AdvisoryDatabase is a provisioned OSV export for dependency scanners;
	// empty refreshes the host cache from the advisory endpoint.
	AdvisoryDatabase string
	ModuleRoot       string
	// HomeDir is the scanner data root. Empty uses the user config directory.
	HomeDir         string
	ProcessPriority exec.ProcessPriority
	// ProjectTierApplies gates project scanner overlays.
	ProjectTierApplies func(context.Context, string) bool
}

// Impl registers and runs code scanners for gate scans.
type Impl struct {
	work        workscope.Group
	lifecycleMu sync.Mutex
	closeOnce   sync.Once
	closeErr    error
	init        sync.Once
	scanners    *catalogruntime.Registry[scan.CodeScanner]
	opts        Options
	home        string
	static      *scancatalog.ScannerConfig

	// merged caches overlay catalogs by their input file stamps.
	mergedMu sync.Mutex
	merged   map[string]mergedCatalog
}

// mergedCatalog is one cached merge and the file stamps it was read from.
type mergedCatalog struct {
	stamps [2]fileStamp
	cfg    *scancatalog.ScannerConfig
}

type fileStamp struct {
	modifiedNS int64
	size       int64
	exists     bool
}

func stampFile(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{modifiedNS: info.ModTime().UnixNano(), size: info.Size(), exists: true}
}

// mergedConfig returns the merged catalog for overlayDir, re-reading it only
// when the user or project scanner file changed.
func (r *Impl) mergedConfig(overlayDir string) (*scancatalog.ScannerConfig, error) {
	stamps := [2]fileStamp{stampFile(scancatalog.UserScannersPath(r.home))}
	if overlayDir != "" {
		stamps[1] = stampFile(scancatalog.ProjectScannersPath(overlayDir))
	}
	r.mergedMu.Lock()
	defer r.mergedMu.Unlock()
	if cached, ok := r.merged[overlayDir]; ok && cached.stamps == stamps {
		return cached.cfg, nil
	}
	cfg, err := scancatalog.LoadMergedScannerConfig(r.opts.ModuleRoot, overlayDir, r.home)
	if err != nil {
		return nil, err
	}
	if r.merged == nil {
		r.merged = make(map[string]mergedCatalog)
	}
	r.merged[overlayDir] = mergedCatalog{stamps: stamps, cfg: cfg}
	return cfg, nil
}

// New loads scanner catalogs and registers available drivers.
func New(ctx context.Context, opts Options) (*Impl, error) {
	cfg, err := scancatalog.LoadMergedScannerConfig(opts.ModuleRoot, "", opts.HomeDir)
	if err != nil {
		return nil, err
	}
	return newFromScannerConfig(ctx, cfg, opts, false)
}

// NewFromScannerConfig registers scanners from an already-loaded catalog.
func NewFromScannerConfig(ctx context.Context, cfg *scancatalog.ScannerConfig, opts Options) (*Impl, error) {
	if err := scancatalog.ValidateScannerConfig(cfg); err != nil {
		return nil, err
	}
	return newFromScannerConfig(ctx, cfg, opts, true)
}

func newFromScannerConfig(ctx context.Context, cfg *scancatalog.ScannerConfig, opts Options, static bool) (*Impl, error) {
	if strings.TrimSpace(opts.ModuleRoot) == "" {
		return nil, fmt.Errorf("module root required")
	}
	home := opts.HomeDir
	var err error
	if home == "" {
		home, err = configdir.UserConfigDir()
		if err != nil {
			return nil, err
		}
	}
	manifest, err := bundled.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("bundled manifest: %w", err)
	}
	if err := bundled.ValidateManifest(manifest); err != nil {
		return nil, err
	}

	values, err := buildScannerGeneration(ctx, cfg, opts, home, manifest)
	if err != nil {
		return nil, err
	}
	reg := &Impl{
		scanners: catalogruntime.NewRegistry[scan.CodeScanner](),
		opts:     opts,
		home:     home,
	}
	if static {
		reg.static = cfg
	}
	reg.scanners.Replace(values)
	return reg, nil
}

func buildScannerGeneration(ctx context.Context, cfg *scancatalog.ScannerConfig, opts Options, home string, manifest *bundled.Manifest) (map[string]scan.CodeScanner, error) {
	values := make(map[string]scan.CodeScanner, len(cfg.Scanners))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, entry := range cfg.Scanners {
		if entry.Driver == scancatalog.DriverExternal && !scancatalog.BinaryOnPath(entry.Command) {
			if entry.SkipIfBinaryMissingOrDefault() {
				continue
			}
			return nil, fmt.Errorf("scanner %q: binary %q not found", entry.ID, entry.Command[0])
		}
		sc, err := newScannerFromEntry(ctx, entry, opts.ModuleRoot, home, manifest, opts)
		if err != nil {
			return nil, fmt.Errorf("scanner %q: %w", entry.ID, err)
		}
		if _, exists := values[sc.ID()]; exists {
			return nil, fmt.Errorf("scanner %q already registered", sc.ID())
		}
		values[sc.ID()] = sc
	}
	return values, nil
}

func (r *Impl) adapters() []scan.CodeScanner {
	reg := r.scannerRegistry()
	var out []scan.CodeScanner
	for _, id := range reg.IDs() {
		if s, ok := reg.Get(id); ok {
			out = append(out, s)
		}
	}
	return out
}

func newScannerFromEntry(ctx context.Context, entry scancatalog.ScannerEntry, moduleRoot, home string, manifest *bundled.Manifest, opts Options) (scan.CodeScanner, error) {
	policy := entry.RuntimePolicy()
	jobs := policy.Parallelism
	prio := opts.ProcessPriority
	if prio == "" {
		prio = exec.ProcessPriorityBelowNormal
	}
	return scannerFactories.Build(ctx, strings.TrimSpace(entry.Driver), scannerBuild{
		fingerprintKey: opts.ScannerFingerprintKey, advisories: opts.AdvisoryDatabase, entry: entry, moduleRoot: moduleRoot,
		home: home, manifest: manifest, jobs: jobs, priority: prio,
	})
}

type scannerBuild struct {
	fingerprintKey []byte
	advisories     string
	entry          scancatalog.ScannerEntry
	moduleRoot     string
	home           string
	manifest       *bundled.Manifest
	jobs           int
	priority       exec.ProcessPriority
}

var scannerFactories = catalogruntime.NewFactorySet(
	map[string]catalogruntime.Factory[scannerBuild, scan.CodeScanner]{
		scancatalog.DriverLibrary: func(_ context.Context, build scannerBuild) (scan.CodeScanner, error) {
			return libraryworker.New(libraryworker.Options{
				FingerprintKey: build.fingerprintKey, AdvisoryDatabase: build.advisories,
				ID: build.entry.ID, Impl: build.entry.Impl, Jobs: build.jobs,
				Categories: build.entry.CategoriesAPI(), ProcessPriority: build.priority,
			}), nil
		},
		scancatalog.DriverBundled: func(_ context.Context, build scannerBuild) (scan.CodeScanner, error) {
			if build.entry.Impl != bundled.ImplOpengrep {
				return nil, fmt.Errorf("unknown bundled impl %q", build.entry.Impl)
			}
			return bundleddriver.NewOpenGrepScanner(bundleddriver.OpenGrepOptions{
				ID:              build.entry.ID,
				HomeDir:         build.home,
				Manifest:        build.manifest,
				Jobs:            build.jobs,
				ProcessPriority: build.priority,
				RuntimePolicy:   build.entry.RuntimePolicy(),
			}), nil
		},
		scancatalog.DriverExternal: func(_ context.Context, build scannerBuild) (scan.CodeScanner, error) {
			return external.NewScanner(build.entry, build.moduleRoot, build.home, build.priority)
		},
	},
	nil,
)

// Register adds a scanner to the registry.
func (r *Impl) Register(s scan.CodeScanner) error {
	ctx, finish, err := r.work.Begin(context.Background())
	if err != nil {
		return err
	}
	defer finish()
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || strings.TrimSpace(s.ID()) == "" {
		return fmt.Errorf("scanner id required")
	}
	if err := r.scannerRegistry().Add(s.ID(), s); err != nil {
		return fmt.Errorf("scanner %q already registered", s.ID())
	}
	return nil
}

func (r *Impl) scannerRegistry() *catalogruntime.Registry[scan.CodeScanner] {
	r.init.Do(func() {
		if r.scanners == nil {
			r.scanners = catalogruntime.NewRegistry[scan.CodeScanner]()
		}
	})
	return r.scanners
}

// Get returns a scanner by id.
func (r *Impl) Get(id string) (scan.CodeScanner, error) {
	s, ok := r.scannerRegistry().Get(id)
	if !ok {
		return nil, fmt.Errorf("scanner %q not found", id)
	}
	return s, nil
}

// List returns the current device selection filtered by categories (empty = all).
func (r *Impl) List(categories ...api.ScanCategory) []scan.ScannerMeta {
	return r.ListForProject(context.Background(), "", categories...)
}

// ListForProject applies trusted project scanner settings.
func (r *Impl) ListForProject(ctx context.Context, projectDir string, categories ...api.ScanCategory) []scan.ScannerMeta {
	cfg := r.static
	if cfg == nil {
		overlayDir := ""
		if strings.TrimSpace(projectDir) != "" && r.opts.ProjectTierApplies != nil && r.opts.ProjectTierApplies(ctx, projectDir) {
			overlayDir = projectDir
		}
		var err error
		cfg, err = r.mergedConfig(overlayDir)
		if err != nil {
			return nil
		}
	}
	var out []scan.ScannerMeta
	for _, entry := range cfg.Scanners {
		if !entry.EnabledOrDefault() {
			continue
		}
		s, ok := r.scannerRegistry().Get(entry.ID)
		if !ok {
			continue
		}
		if len(categories) > 0 && !categoryOverlap(s.Categories(), categories) {
			continue
		}
		out = append(out, scan.ScannerMeta{
			ID:         s.ID(),
			Categories: s.Categories(),
			Name:       s.ID(),
			Contract:   entry.Contract(),
		})
	}
	return out
}

// RunBest selects the best enabled scanner for requested categories and runs it.
func (r *Impl) RunBest(ctx context.Context, categories []api.ScanCategory, req scan.ScanRequest) (*scanoutput.Result, error) {
	ctx, finish, err := r.work.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if len(categories) == 0 {
		return nil, fmt.Errorf("categories required")
	}
	if strings.TrimSpace(req.ProjectDir) == "" {
		return nil, fmt.Errorf("project dir required")
	}

	candidates := r.ListForProject(ctx, req.ProjectDir, categories...)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w %v", scancatalog.ErrNoScannerForCategories, categories)
	}
	best := pickBestScanner(candidates, categories)
	sc, err := r.Get(best.ID)
	if err != nil {
		return nil, err
	}
	req.Categories = categories
	req.ScannerID = best.ID
	return sc.Run(ctx, req)
}

func pickBestScanner(candidates []scan.ScannerMeta, want []api.ScanCategory) scan.ScannerMeta {
	best := candidates[0]
	bestScore := scoreScanner(best, want)
	for _, c := range candidates[1:] {
		if sc := scoreScanner(c, want); sc > bestScore {
			best = c
			bestScore = sc
		}
	}
	return best
}

func scoreScanner(meta scan.ScannerMeta, want []api.ScanCategory) int {
	score := 0
	for _, w := range want {
		for _, c := range meta.Categories {
			if c == w {
				score++
			}
		}
	}
	return score
}

func categoryOverlap(have, want []api.ScanCategory) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}
