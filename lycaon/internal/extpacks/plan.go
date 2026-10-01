package extpacks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PackagePlan is one prepared device package mutation.
type PackagePlan struct {
	Roots       []DesiredPack
	RemoveIDs   []string
	RebuildLock bool // start from an empty lock instead of the current one
	Resolution  PackageResolution
	PackageRoot string
	Warnings    []string
	Meta        *InstallMetaResult

	graph      map[string]packageCandidate
	cleanups   []map[string]packageCandidate
	stagingDir string
	cacheRoot  *metaRootTransaction
}

// Close removes staging clones retained for the plan's lifetime.
func (p *PackagePlan) Close() {
	if p == nil {
		return
	}
	cleanupCandidates(p.graph)
	for _, graph := range p.cleanups {
		cleanupCandidates(graph)
	}
	if p.cacheRoot != nil {
		p.cacheRoot.closeUnbound()
	}
	if p.stagingDir != "" {
		_ = os.RemoveAll(p.stagingDir)
	}
}

// Apply computes the candidate device state.
func (p *PackagePlan) Apply(desired DesiredState, lock LockFile) (DesiredState, LockFile, error) {
	if len(p.RemoveIDs) > 0 {
		for _, id := range p.RemoveIDs {
			desired = dropPackFromDesired(desired, id)
			lock = dropLockedPackage(lock, id)
		}
		lock = retainReachablePackages(lock, desired)
		return desired, lock, nil
	}
	if p.RebuildLock {
		lock = EmptyLock()
	}
	for _, root := range p.Roots {
		desired = setPackRow(desired, root)
	}
	for _, candidate := range p.graph {
		lock = upsertLockedPackage(lock, candidateLockedPackage(candidate, p.graph))
	}
	lock = retainReachablePackages(lock, desired)
	return desired, lock, nil
}

// BindCacheRoots records the state bytes that make a prepared cache root live.
func (p *PackagePlan) BindCacheRoots(desiredPath, lockPath string, desired, lock []byte) error {
	if p == nil || p.cacheRoot == nil {
		return nil
	}
	return p.cacheRoot.bind(desiredPath, lockPath, desired, lock)
}

// PublishCacheRoots atomically promotes prepared cache roots before state.
func (p *PackagePlan) PublishCacheRoots() error {
	if p == nil || p.cacheRoot == nil {
		return nil
	}
	return p.cacheRoot.publish()
}

// RollbackCacheRoots restores the selected cache root and discards the candidate.
func (p *PackagePlan) RollbackCacheRoots() error {
	if p == nil || p.cacheRoot == nil {
		return nil
	}
	return p.cacheRoot.rollback()
}

// FinalizeCacheRoots discards rollback material after state publication.
func (p *PackagePlan) FinalizeCacheRoots() error {
	if p == nil || p.cacheRoot == nil {
		return nil
	}
	return p.cacheRoot.finalize()
}

func storePlanCandidates(graph map[string]packageCandidate) error {
	for _, candidate := range graph {
		if err := storeReleaseCandidate(candidate); err != nil {
			return err
		}
	}
	return nil
}

// PrepareInstall resolves one install source into a package plan.
func PrepareInstall(ctx context.Context, opts InstallOptions) (*PackagePlan, error) {
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		return nil, fmt.Errorf("extensions install: source required")
	}
	ref := strings.TrimSpace(opts.Ref)
	version := strings.TrimSpace(opts.Version)
	if ref != "" && version != "" {
		return nil, fmt.Errorf("extensions install: version and ref are mutually exclusive")
	}
	if abs, linked, err := resolveLinkedLocalSource(source, opts.ProjectDir); err != nil {
		return nil, err
	} else if linked {
		hasExt := fileExists(filepath.Join(abs, "extension.yaml"))
		hasMeta := fileExists(filepath.Join(abs, MetaPackFileName))
		switch {
		case hasMeta && hasExt:
			return nil, ErrAmbiguousPackRoot
		case hasMeta && !hasExt:
			return nil, fmt.Errorf("%w", ErrUseInstallMeta)
		case !hasExt:
			return nil, fmt.Errorf("extensions install: extension.yaml required")
		}
		if err := refuseLinkIntoManagedExtensionCaches(abs); err != nil {
			return nil, err
		}
		plan, err := prepareDevelopmentInstall(ctx, abs, source, opts.ProjectDir)
		if err != nil {
			return nil, err
		}
		plan.Warnings = append(plan.Warnings, packRequirementWarnings(ctx, abs, opts)...)
		return plan, nil
	}

	var probe map[string]packageCandidate
	var packID string
	var err error
	if ref != "" {
		probe, packID, err = resolveExactGraph(ctx, source, ref, opts.ProjectDir)
	} else {
		if version == "" {
			version = "*"
		}
		probe, packID, err = resolveReleaseGraph(ctx, source, version, opts.ProjectDir)
	}
	if err != nil {
		return nil, fmt.Errorf("extensions install: %w", err)
	}
	if IsStockPackID(packID) {
		cleanupCandidates(probe)
		return nil, fmt.Errorf("extensions install: refusing to cache %q: %w", packID, ErrStockPack)
	}
	desired := DesiredPack{ID: packID, Source: source, Version: version, Ref: ref, Enabled: boolPtr(true)}
	roots, graph, err := resolveDeviceGraph(ctx, opts.ProjectDir, []DesiredPack{desired}, probe, []string{packID})
	if err != nil {
		cleanupCandidates(probe)
		return nil, fmt.Errorf("extensions install: resolve scope: %w", err)
	}
	if err := storePlanCandidates(graph); err != nil {
		cleanupCandidates(probe)
		cleanupCandidates(graph)
		return nil, err
	}
	root := graph[packID]
	packageRoot, _ := CachedPackRevisionDir(packID, root.Revision)
	return &PackagePlan{
		Roots:    roots,
		cleanups: []map[string]packageCandidate{probe},
		Resolution: PackageResolution{
			PackID: packID, Version: root.Manifest.Version,
			ResolvedRevision: root.Revision, Integrity: root.Integrity,
			ExtensionAPI: root.Manifest.Compatibility.ExtensionAPI, Kind: PackKindGit,
		},
		PackageRoot: packageRoot,
		Warnings:    packRequirementWarnings(ctx, root.Root, opts),
		graph:       graph,
	}, nil
}

func prepareDevelopmentInstall(ctx context.Context, abs, source, projectDir string) (*PackagePlan, error) {
	probe, packID, err := resolveDevelopmentGraph(ctx, abs, projectDir)
	if err != nil {
		return nil, fmt.Errorf("path link: %w", err)
	}
	if IsStockPackID(packID) {
		cleanupCandidates(probe)
		return nil, fmt.Errorf("path link: refusing stock id %q: %w", packID, ErrStockPack)
	}
	if strings.HasPrefix(source, "path:") {
		source = "path:" + abs
	}
	desired := DesiredPack{ID: packID, Source: source, Development: true, Enabled: boolPtr(true)}
	roots, graph, err := resolveDeviceGraph(ctx, projectDir, []DesiredPack{desired}, probe, []string{packID})
	if err != nil {
		cleanupCandidates(probe)
		return nil, fmt.Errorf("path link: resolve scope: %w", err)
	}
	if err := storePlanCandidates(graph); err != nil {
		cleanupCandidates(probe)
		cleanupCandidates(graph)
		return nil, err
	}
	root := graph[packID]
	return &PackagePlan{
		Roots:    roots,
		cleanups: []map[string]packageCandidate{probe},
		Resolution: PackageResolution{
			PackID: packID, Version: root.Manifest.Version,
			Integrity: root.Integrity, ExtensionAPI: root.Manifest.Compatibility.ExtensionAPI,
			Kind: PackKindPath,
		},
		PackageRoot: abs,
		graph:       graph,
	}, nil
}

// PrepareRemoval validates a batch against one device manifest.
func PrepareRemoval(packIDs []string) (*PackagePlan, error) {
	if len(packIDs) == 0 {
		return nil, fmt.Errorf("extensions remove: pack ids required")
	}
	seen := make(map[string]bool, len(packIDs))
	for _, id := range packIDs {
		if err := ValidatePackID(id); err != nil {
			return nil, fmt.Errorf("extensions remove: %w", err)
		}
		if seen[id] {
			return nil, fmt.Errorf("extensions remove: duplicate pack id %q", id)
		}
		if IsStockPackID(id) {
			return nil, fmt.Errorf("extensions remove: cannot remove stock package %q: %w", id, ErrStockPack)
		}
		seen[id] = true
	}
	desiredPath, err := DeviceDesiredPath()
	if err != nil {
		return nil, fmt.Errorf("extensions remove: %w", err)
	}
	desired, err := LoadDesiredFile(desiredPath)
	if err != nil {
		return nil, fmt.Errorf("extensions remove: %w", err)
	}
	for _, id := range packIDs {
		if _, ok := DesiredPackRow(desired, id); !ok {
			return nil, fmt.Errorf("extensions remove: pack %s is not a direct device installation", id)
		}
	}
	return &PackagePlan{RemoveIDs: append([]string{}, packIDs...)}, nil
}

// PrepareUpdate resolves the newest admissible graph for one released pack.
func PrepareUpdate(ctx context.Context, packID string) (*PackagePlan, error) {
	_, _, root, err := packageUpdateState(packID)
	if err != nil {
		return nil, err
	}
	if root.Development {
		return nil, fmt.Errorf("update: development pack — use reload")
	}
	roots, graph, err := resolveDeviceGraph(ctx, "", nil, nil, []string{packID})
	if err != nil {
		return nil, err
	}
	if _, ok := graph[packID]; !ok {
		cleanupCandidates(graph)
		return nil, fmt.Errorf("update: resolved scope omits %q", packID)
	}
	if err := storePlanCandidates(graph); err != nil {
		cleanupCandidates(graph)
		return nil, err
	}
	candidate := graph[packID]
	return &PackagePlan{
		Roots: roots,
		Resolution: PackageResolution{
			PackID: packID, Version: candidate.Manifest.Version,
			ResolvedRevision: candidate.Revision, Integrity: candidate.Integrity,
			ExtensionAPI: candidate.Manifest.Compatibility.ExtensionAPI, Kind: PackKindGit,
		},
		graph: graph,
	}, nil
}

// PrepareReload republishes a linked development graph with fresh integrity.
func PrepareReload(ctx context.Context, packID string) (*PackagePlan, error) {
	_, _, root, err := packageUpdateState(packID)
	if err != nil {
		return nil, err
	}
	if !root.Development {
		return nil, fmt.Errorf("reload: only development packs support reload")
	}
	abs, linked, err := resolveLinkedLocalSource(root.Source, "")
	if err != nil || !linked {
		return nil, fmt.Errorf("reload: %w", ErrLinkedSourceMissing)
	}
	roots, graph, err := resolveDeviceGraph(ctx, "", nil, nil, []string{packID})
	if err != nil {
		return nil, fmt.Errorf("reload: %w", err)
	}
	if _, ok := graph[packID]; !ok {
		cleanupCandidates(graph)
		return nil, fmt.Errorf("reload: resolved scope omits %q", packID)
	}
	if err := storePlanCandidates(graph); err != nil {
		cleanupCandidates(graph)
		return nil, err
	}
	candidate := graph[packID]
	return &PackagePlan{
		Roots: roots,
		Resolution: PackageResolution{
			PackID: packID, Version: candidate.Manifest.Version,
			Integrity: candidate.Integrity, ExtensionAPI: candidate.Manifest.Compatibility.ExtensionAPI,
			Kind: PackKindPath,
		},
		PackageRoot: abs,
		Warnings:    packRequirementWarnings(ctx, abs, InstallOptions{}),
		graph:       graph,
	}, nil
}
