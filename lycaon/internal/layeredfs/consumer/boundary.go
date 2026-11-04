package consumer

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
)

// EngineConfig configures a consumer Engine for a worker view.
type EngineConfig struct {
	ViewID           string
	Core             *layeredfs.Core
	PreparedView     *adapter.PreparedView
	Adapter          adapter.Adapter
	Roots            []layeredfs.RootSpec
	MountedRootPaths map[string]string
	BackingDir       string
	LowerSourcePaths map[string]string
	CheckpointStore  *checkpoint.Store
	Tracker          *observation.Tracker
	Index            SourceIndexProvider
}

// Engine coordinates indexed source inventory, private change overlays, mount-enforced
// live reads, checkpoint inspection, and bounded search for a worker view (#1664).
type Engine struct {
	viewID       string
	core         *layeredfs.Core
	preparedView *adapter.PreparedView
	adp          adapter.Adapter
	rootsByID    map[string]layeredfs.RootSpec
	rootOrder    []string
	mountedPaths map[string]string
	backingDir   string
	lowerPaths   map[string]string
	ckptStore    *checkpoint.Store
	tracker      *observation.Tracker
	index        SourceIndexProvider
}

// NewEngine constructs a consumer Engine and initializes its access-boundary guard and observation tracker.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	viewID := strings.TrimSpace(cfg.ViewID)
	core := cfg.Core
	if cfg.PreparedView != nil {
		if viewID == "" {
			viewID = cfg.PreparedView.ViewID
		}
		if core == nil {
			core = cfg.PreparedView.Core
		}
	}
	if viewID == "" {
		viewID = "worker-view"
	}

	rootsByID := make(map[string]layeredfs.RootSpec)
	var rootOrder []string
	mountedPaths := make(map[string]string)
	lowerPaths := make(map[string]string)

	for _, r := range cfg.Roots {
		if r.ID == "" {
			continue
		}
		rootsByID[r.ID] = r
		rootOrder = append(rootOrder, r.ID)
		if r.TrunkPath != "" {
			lowerPaths[r.ID] = filepath.Clean(r.TrunkPath)
		}
	}
	if cfg.PreparedView != nil {
		for _, pr := range cfg.PreparedView.Roots {
			if pr.RootID == "" {
				continue
			}
			if _, exists := rootsByID[pr.RootID]; !exists {
				rootsByID[pr.RootID] = layeredfs.RootSpec{
					ID: pr.RootID, Label: pr.Label, TrunkPath: pr.SourcePath, Subroot: pr.Subroot, IsPrimary: pr.IsPrimary,
				}
				rootOrder = append(rootOrder, pr.RootID)
			}
			if pr.WorkspacePath != "" {
				mountedPaths[pr.RootID] = filepath.Clean(pr.WorkspacePath)
			}
			if pr.SourcePath != "" {
				lowerPaths[pr.RootID] = filepath.Clean(pr.SourcePath)
			}
		}
	}
	for k, v := range cfg.MountedRootPaths {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			mountedPaths[k] = filepath.Clean(v)
		}
	}
	for k, v := range cfg.LowerSourcePaths {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			lowerPaths[k] = filepath.Clean(v)
		}
	}
	backingDir := strings.TrimSpace(cfg.BackingDir)
	if backingDir == "" && cfg.PreparedView != nil {
		backingDir = strings.TrimSpace(cfg.PreparedView.BackingDir)
	}
	if backingDir != "" {
		backingDir = filepath.Clean(backingDir)
	}

	tracker := cfg.Tracker
	if tracker == nil {
		facts := observation.UnknownVisibilityFacts("unbound")
		if cfg.PreparedView != nil {
			facts = observation.VisibilityFactsFromDescriptor(observation.ViewCapabilitiesFromPreparedView(cfg.PreparedView))
		} else if cfg.Adapter != nil {
			facts = observation.VisibilityFactsFromDescriptor(observation.ViewCapabilitiesFromAdapterFacts(viewID, cfg.Adapter.Facts()))
		}
		var rootSpecs []layeredfs.RootSpec
		for _, id := range rootOrder {
			rootSpecs = append(rootSpecs, rootsByID[id])
		}
		var err error
		tracker, err = observation.NewTracker(observation.TrackerConfig{
			ViewID: viewID, Facts: facts, Core: core, Roots: rootSpecs,
		})
		if err != nil {
			return nil, err
		}
	}

	eng := &Engine{
		viewID: viewID, core: core, preparedView: cfg.PreparedView, adp: cfg.Adapter,
		rootsByID: rootsByID, rootOrder: rootOrder, mountedPaths: mountedPaths,
		backingDir: backingDir, lowerPaths: lowerPaths, ckptStore: cfg.CheckpointStore,
		tracker: tracker, index: cfg.Index,
	}
	eng.registerViewPaths()
	return eng, nil
}

func (e *Engine) registerViewPaths() {
	if e.preparedView != nil {
		RegisterBranchEngine(e.preparedView.PrimaryWorkPath, e)
		RegisterBranchEngine(e.preparedView.RuntimeViewDir, e)
	}
	for _, mnt := range e.mountedPaths {
		RegisterBranchEngine(mnt, e)
	}
}

// Tracker returns the Engine's observation.Tracker.
func (e *Engine) Tracker() *observation.Tracker { return e.tracker }

// VisibilityFacts returns the view's explicit visibility and cache coherence facts.
func (e *Engine) VisibilityFacts() observation.VisibilityFacts {
	if e.tracker == nil {
		return observation.UnknownVisibilityFacts("unbound")
	}
	return e.tracker.VisibilityFacts()
}

// GuardLiveConsumerPath enforces the structural contract that live consumers must read
// through the mounted workspace view and never bypass it via BackingDir or raw lower paths.
func (e *Engine) GuardLiveConsumerPath(rootID, candidateAbsPath string) error {
	cleanCandidate := filepath.Clean(strings.TrimSpace(candidateAbsPath))
	if cleanCandidate == "" || !filepath.IsAbs(cleanCandidate) {
		return fmt.Errorf("%w: non-absolute path %q", ErrDirectBackingAccessForbidden, candidateAbsPath)
	}
	if e.backingDir != "" && hasPathPrefix(cleanCandidate, e.backingDir) {
		return fmt.Errorf("%w: backing path %q", ErrDirectBackingAccessForbidden, cleanCandidate)
	}
	mntRoot, hasMount := e.mountedPaths[rootID]
	if lowerRoot, hasLower := e.lowerPaths[rootID]; hasLower && lowerRoot != "" {
		if (!hasMount || mntRoot != lowerRoot) && hasPathPrefix(cleanCandidate, lowerRoot) {
			return fmt.Errorf("%w: direct lower path %q", ErrDirectBackingAccessForbidden, cleanCandidate)
		}
	}
	if !hasMount || mntRoot == "" {
		return ErrMountNotConfigured
	}
	if !hasPathPrefix(cleanCandidate, mntRoot) {
		return fmt.Errorf("%w: %q is outside mounted root %q", ErrPathEscape, cleanCandidate, mntRoot)
	}
	return nil
}

// ResolveMountedPath resolves a root-qualified path to its absolute path inside the worker mount
// and verifies it passes GuardLiveConsumerPath.
func (e *Engine) ResolveMountedPath(qpath layeredfs.QualifiedPath) (string, error) {
	nq, err := normalizeQualifiedPath(qpath)
	if err != nil {
		return "", err
	}
	mntRoot, ok := e.mountedPaths[nq.RootID]
	if !ok || mntRoot == "" {
		return "", fmt.Errorf("%w: %s", ErrUnknownRoot, nq.RootID)
	}
	target := mntRoot
	if nq.Rel != "." {
		target = filepath.Join(mntRoot, filepath.FromSlash(nq.Rel))
	}
	if err := e.GuardLiveConsumerPath(nq.RootID, target); err != nil {
		return "", err
	}
	return target, nil
}

// IsInternalStoragePath reports whether rel is an internal storage or VCS metadata path.
func IsInternalStoragePath(rel string) bool {
	cleaned := path.Clean(strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), "/"))
	if cleaned == "." || cleaned == "" {
		return false
	}
	for _, seg := range strings.Split(cleaned, "/") {
		switch seg {
		case ".git", ".lycaon-work", ".paintedwolf", ".provision.lock":
			return true
		}
	}
	return false
}

func normalizeQualifiedPath(qpath layeredfs.QualifiedPath) (layeredfs.QualifiedPath, error) {
	rootID := strings.TrimSpace(qpath.RootID)
	if rootID == "" {
		return layeredfs.QualifiedPath{}, ErrUnknownRoot
	}
	rawRel := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(qpath.Rel)), "/")
	if rawRel == "" {
		rawRel = "."
	}
	cleaned := path.Clean(rawRel)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return layeredfs.QualifiedPath{}, fmt.Errorf("%w: %q", ErrPathEscape, qpath.Rel)
	}
	return layeredfs.QualifiedPath{RootID: rootID, Rel: cleaned}, nil
}

func hasPathPrefix(target, prefix string) bool {
	target, prefix = filepath.Clean(target), filepath.Clean(prefix)
	return target == prefix || strings.HasPrefix(target, prefix+string(filepath.Separator))
}

func isDescendantRel(rel, ancestor string) bool {
	if ancestor == "." || ancestor == "" {
		return rel != "." && rel != ""
	}
	return strings.HasPrefix(rel, ancestor+"/")
}

func sortInventoryEntries(entries []InventoryEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path.RootID != entries[j].Path.RootID {
			return entries[i].Path.RootID < entries[j].Path.RootID
		}
		return entries[i].Path.Rel < entries[j].Path.Rel
	})
}

func entryKindFromMode(m fs.FileMode) layeredfs.EntryKind {
	switch {
	case m.IsDir():
		return layeredfs.EntryDirectory
	case m&fs.ModeSymlink != 0:
		return layeredfs.EntrySymlink
	default:
		return layeredfs.EntryRegular
	}
}
