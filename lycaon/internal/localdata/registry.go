package localdata

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scratch"
)

// ExtensionCacheClear serializes deletion with extension mutations.
type ExtensionCacheClear func(ctx context.Context, clearStorage func() error) error

// SourceCatalogClear serializes deletion with catalog readers and writers.
type SourceCatalogClear func(ctx context.Context, clearStorage func() error) error

// Registry resolves catalog buckets under a config root.
type Registry struct {
	baseDir                 string
	clearWebIndex           func(context.Context) error
	clearSourceObservations func(context.Context) error
	clearSourceCatalog      SourceCatalogClear
	sourceCatalogSpilled    func() int64
	clearExtensions         ExtensionCacheClear
	workerBranchesStatus    WorkerBranchesStatus
	reclaimWorkerBranches   func(context.Context) error
	reclaimSessionScratch   func(context.Context) error
}

// WorkerBranchesStatus reports the branch trees on disk and their allocated size.
type WorkerBranchesStatus func(context.Context) (present bool, bytes int64, err error)

// SetWorkerBranches configures disk accounting and sealed-tree reclamation.
func (r *Registry) SetWorkerBranches(status WorkerBranchesStatus, reclaim func(context.Context) error) {
	if r == nil {
		return
	}
	r.workerBranchesStatus = status
	r.reclaimWorkerBranches = reclaim
}

// SetSessionScratchReclaim configures clearing that keeps the scratch of
// sessions still in use.
func (r *Registry) SetSessionScratchReclaim(reclaim func(context.Context) error) {
	if r != nil {
		r.reclaimSessionScratch = reclaim
	}
}

// SetSourceObservationsClear configures observation-cache clearing.
func (r *Registry) SetSourceObservationsClear(fn func(context.Context) error) {
	if r != nil {
		r.clearSourceObservations = fn
	}
}

// SetSourceCatalogClear serializes disk cleanup with the catalog lifecycle.
func (r *Registry) SetSourceCatalogClear(fn SourceCatalogClear) {
	if r != nil {
		r.clearSourceCatalog = fn
	}
}

// SetSourceCatalogSpilled reports bytes held in spill files. They have no
// directory entry, so a size walk alone under-reports the bucket.
func (r *Registry) SetSourceCatalogSpilled(fn func() int64) {
	if r != nil {
		r.sourceCatalogSpilled = fn
	}
}

// SetExtensionCacheClear serializes clearing with extension writes.
func (r *Registry) SetExtensionCacheClear(fn ExtensionCacheClear) {
	if r == nil {
		return
	}
	r.clearExtensions = fn
}

// New builds a registry rooted at baseDir (empty → configdir.UserConfigDir).
func New(baseDir string) (*Registry, error) {
	base := strings.TrimSpace(baseDir)
	if base == "" {
		var err error
		base, err = configdir.UserConfigDir()
		if err != nil {
			return nil, err
		}
	}
	base = filepath.Clean(base)
	return &Registry{baseDir: base}, nil
}

// SetWebIndexClear configures web-index clearing.
func (r *Registry) SetWebIndexClear(fn func(context.Context) error) {
	if r == nil {
		return
	}
	r.clearWebIndex = fn
}

// BaseDir returns the config root used for path resolution.
func (r *Registry) BaseDir() string {
	if r == nil {
		return ""
	}
	return r.baseDir
}

// Status reports a bucket's presence and disk usage.
type Status struct {
	ID      string
	Present bool
	Bytes   int64
}

// ClearResult is the per-bucket outcome for a clear request.
type ClearResult struct {
	ID    string
	OK    bool
	Error string
}

// StatusAll returns status in catalog order.
func (r *Registry) StatusAll(ctx context.Context) ([]Status, error) {
	out := make([]Status, 0, len(catalogOrder))
	for _, id := range catalogOrder {
		st, err := r.Status(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// Status reports presence and size for one known bucket id.
func (r *Registry) Status(ctx context.Context, id string) (Status, error) {
	if r == nil {
		return Status{}, fmt.Errorf("localdata registry nil")
	}
	st := Status{ID: id}
	var err error
	switch id {
	case BucketWebIndex:
		p := webIndexPath(r.baseDir)
		st.Present = pathExists(p)
		st.Bytes, err = dirOrFileBytes(p)
	case BucketSourceObservations:
		for _, path := range sourceObservationPaths(r.baseDir) {
			st.Present = st.Present || pathExists(path)
		}
		st.Bytes, err = sourceObservationsBytes(r.baseDir)
	case BucketSourceCatalog:
		p := enginepaths.SourceCatalogCacheRootUnder(r.baseDir)
		st.Bytes, err = dirOrFileBytes(p)
		if r.sourceCatalogSpilled != nil {
			st.Bytes += r.sourceCatalogSpilled()
		}
		st.Present = st.Bytes > 0
	case BucketFetchCache:
		p := enginepaths.FetchCacheRootUnder(r.baseDir)
		st.Present = dirNonEmpty(p)
		st.Bytes, err = dirOrFileBytes(p)
	case BucketOSVCache:
		p := project.OSVCacheDir(r.baseDir)
		st.Present = dirNonEmpty(p)
		st.Bytes, err = dirOrFileBytes(p)
	case BucketModelfeed:
		p := enginepaths.ModelfeedRootUnder(r.baseDir)
		st.Present = dirNonEmpty(p) || pathExists(filepath.Join(p, "api.json"))
		st.Bytes, err = dirOrFileBytes(p)
	case BucketPricingCache:
		p := enginepaths.PricingCacheRootUnder(r.baseDir)
		st.Present = dirNonEmpty(p)
		st.Bytes, err = dirOrFileBytes(p)
	case BucketBrowserCache:
		p := enginepaths.BrowserCacheRootUnder(r.baseDir)
		st.Present = dirNonEmpty(p)
		st.Bytes, err = dirOrFileBytes(p)
	case BucketExtensionCache:
		st.Present = dirNonEmpty(enginepaths.ExtensionsCacheRootUnder(r.baseDir)) || dirNonEmpty(enginepaths.ExtensionsMetaRootUnder(r.baseDir))
		st.Bytes, err = extensionCacheBytes(r.baseDir)
	case BucketDebugLogs:
		st.Present = r.debugLogsPresent()
		st.Bytes, err = debugLogsBytes(r.baseDir)
	case BucketScanScratch:
		st.Present = pathExists(enginepaths.VerifyDetectPathUnder(r.baseDir))
		st.Bytes, err = scanScratchBytes(r.baseDir)
	case BucketWorkerBranches:
		if r.workerBranchesStatus != nil {
			st.Present, st.Bytes, err = r.workerBranchesStatus(ctx)
			break
		}
		p := enginepaths.WorkerBranchesRootUnder(r.baseDir)
		st.Present = dirNonEmpty(p)
		st.Bytes, err = dirOrFileBytes(p)
	case BucketSessionScratch:
		st.Present, st.Bytes, err = scratch.New(r.baseDir).Inventory(ctx)
	default:
		return Status{}, fmt.Errorf("unknown local-data bucket %q", id)
	}
	return st, err
}

func (r *Registry) debugLogsPresent() bool {
	return dirNonEmpty(debugpaths.DebugRootUnder(r.baseDir)) ||
		pathExists(debugpaths.DebugRootUnder(r.baseDir))
}

// ClearOne clears a single known bucket. Unknown ids error without side effects.
func (r *Registry) ClearOne(ctx context.Context, id string) error {
	if r == nil {
		return fmt.Errorf("localdata registry nil")
	}
	switch id {
	case BucketWebIndex:
		if r.clearWebIndex == nil {
			// A closed index has no live writer to coordinate with.
			return clearTree(r.baseDir, webIndexPath(r.baseDir))
		}
		return r.clearWebIndex(ctx)
	case BucketSourceObservations:
		if r.clearSourceObservations == nil {
			return clearSourceObservations(r.baseDir)
		}
		if err := r.clearSourceObservations(ctx); err != nil {
			return err
		}
		return clearTree(r.baseDir, enginepaths.RepoOrientationRootUnder(r.baseDir))
	case BucketSourceCatalog:
		clear := func() error {
			return clearDirContents(r.baseDir, enginepaths.SourceCatalogCacheRootUnder(r.baseDir))
		}
		if r.clearSourceCatalog != nil {
			return r.clearSourceCatalog(ctx, clear)
		}
		return clear()
	case BucketFetchCache:
		return clearDirContents(r.baseDir, enginepaths.FetchCacheRootUnder(r.baseDir))
	case BucketOSVCache:
		return clearDirContents(r.baseDir, project.OSVCacheDir(r.baseDir))
	case BucketModelfeed:
		return clearDirContents(r.baseDir, enginepaths.ModelfeedRootUnder(r.baseDir))
	case BucketPricingCache:
		return clearDirContents(r.baseDir, enginepaths.PricingCacheRootUnder(r.baseDir))
	case BucketBrowserCache:
		return clearDirContents(r.baseDir, enginepaths.BrowserCacheRootUnder(r.baseDir))
	case BucketExtensionCache:
		if r.clearExtensions != nil {
			return r.clearExtensions(ctx, func() error { return clearExtensionCache(r.baseDir) })
		}
		return clearExtensionCache(r.baseDir)
	case BucketDebugLogs:
		return clearDebugLogs(r.baseDir)
	case BucketScanScratch:
		return clearScanScratch(r.baseDir)
	case BucketWorkerBranches:
		if r.reclaimWorkerBranches != nil {
			return r.reclaimWorkerBranches(ctx)
		}
		// Without an engine to judge the trees, only an idle root is safe to clear.
		return clearDirContents(r.baseDir, enginepaths.WorkerBranchesRootUnder(r.baseDir))
	case BucketSessionScratch:
		if r.reclaimSessionScratch != nil {
			return r.reclaimSessionScratch(ctx)
		}
		// Without an engine no session is in use, so every folder is clearable.
		return clearDirContents(r.baseDir, enginepaths.ScratchRootUnder(r.baseDir))
	default:
		return fmt.Errorf("unknown local-data bucket %q", id)
	}
}

// ClearBuckets clears known buckets in request order.
func (r *Registry) ClearBuckets(ctx context.Context, ids []string) ([]ClearResult, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("buckets required")
	}
	for _, id := range ids {
		if !Known(id) {
			return nil, fmt.Errorf("unknown local-data bucket %q", id)
		}
	}
	results := make([]ClearResult, 0, len(ids))
	for _, id := range ids {
		err := r.ClearOne(ctx, id)
		res := ClearResult{ID: id, OK: err == nil}
		if err != nil {
			res.Error = err.Error()
		}
		results = append(results, res)
	}
	return results, nil
}
