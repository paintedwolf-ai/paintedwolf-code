package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcescope"
)

// Root and scoped generations have independent retention limits.
const (
	defaultRootRecordLimit   = 8
	defaultScopedRecordLimit = 24
	// defaultByteBudget bounds retained entry text across both pools.
	defaultByteBudget    = 192 << 20
	maxRootBuildAttempts = 4
)

// detachedRefreshMaxWait bounds speculative refresh admission.
const detachedRefreshMaxWait = 30 * time.Second

type record struct {
	projectID string
	signature string
	roots     []Root
	scoped    bool
	snapshot  Snapshot
	// bytes is the retained size of the published generation, zero while cold.
	bytes               int64
	stale               bool
	mustAdvanceRevision bool
	building            bool
	done                chan struct{}
	cancel              context.CancelFunc
	lastUsed            time.Time
	// validatedAt is the last successful filesystem walk. It bounds reuse when
	// watcher coverage is incomplete and epoch equality cannot prove freshness.
	validatedAt time.Time
	// observed is the newest epoch whose change this catalog has classified;
	// what the change touched is recorded in dirtyPaths or fullReconcile.
	observed      repochange.Epoch
	dirtyPaths    map[string]struct{}
	fullReconcile bool
}

// settle ends the record's build. The build context carries its caller's
// values, such as an HTTP request and the host serving it; the record outlives both.
func (r *record) settle() {
	r.building = false
	r.cancel()
	r.cancel = nil
	close(r.done)
}

// ScopeProvider supplies walk budgets and traversal order without excluding paths.
type ScopeProvider interface {
	Catalog(ctx context.Context, root string) *sourcescope.Scope
}

// Catalog coalesces source scans and atomically publishes immutable snapshots.
type Catalog struct {
	Directories *Directories
	Trees       *TreeStores
	mu          sync.Mutex
	records     map[string]*record
	revision    uint64
	// rootLimit and scopedLimit bound each pool's record count.
	rootLimit   int
	scopedLimit int
	byteBudget  int64
	now         func() time.Time
	build       func(context.Context, []Root, walkPolicy) (Snapshot, error)
	Literals    *LiteralSearch
}

// SetScopes replaces bundled budgets and traversal priority with a provider.
func (c *Catalog) SetScopes(scopes ScopeProvider) {
	if c == nil || scopes == nil {
		return
	}
	c.Trees.scopesMu.Lock()
	c.Trees.scopes = scopes
	c.Trees.scopesMu.Unlock()
}

func New() *Catalog {
	catalog := &Catalog{
		records:     make(map[string]*record),
		rootLimit:   defaultRootRecordLimit,
		scopedLimit: defaultScopedRecordLimit,
		byteBudget:  defaultByteBudget,
		now:         time.Now, build: buildSnapshot,
		Literals: &LiteralSearch{cache: newLiteralIndexCache(), broker: backgroundwork.Process()},
	}
	catalog.Trees = &TreeStores{broker: backgroundwork.Process(), limit: defaultRootRecordLimit + defaultScopedRecordLimit}
	catalog.Directories = &Directories{trees: catalog.Trees}
	catalog.Trees.Directories = catalog.Directories
	return catalog
}

var (
	processOnce sync.Once
	process     *Catalog
)

// Process returns the source catalog shared by HTTP and native tools.
func Process() *Catalog {
	processOnce.Do(func() {
		process = New()
		repochange.RegisterObserver(func(_ context.Context, event repochange.Event) {
			switch event.Kind {
			case repochange.WorktreeChanged:
				process.InvalidateRootChange(event.ProjectDir, event.Paths, repochange.StructuralPaths(event)) //nolint:contextcheck // Catalog refresh outlives event delivery.
			case repochange.HeadMoved:
				if !repochange.Coverage(event.ProjectDir).Complete {
					process.InvalidateRoot(event.ProjectDir) //nolint:contextcheck // Catalog refresh outlives event delivery.
					return
				}
				// Complete watcher coverage supplies the worktree events for a ref move.
				process.observeEpoch(event.ProjectDir)
			case repochange.IndexChanged:
				// An index write leaves the tree as it was.
				process.observeEpoch(event.ProjectDir)
			}
		})
	})
	return process
}

// OpenDependencyIndex builds a fresh request-owned index without expanding the eager catalog.
// Closing its reader joins its preparation and removes the private index files.
func (c *TreeStores) OpenDependencyIndex(ctx context.Context, projectID string, root Root, selectedPaths ...string) (*IndexReader, error) {
	for _, selected := range selectedPaths {
		clean := filepath.ToSlash(filepath.Clean(selected))
		if filepath.IsAbs(selected) || clean != selected || clean == "." || clean == ".." || hasParentPrefix(clean) {
			return nil, os.ErrPermission
		}
	}
	dir, err := os.MkdirTemp("", "paintedwolf-dependency-search-*")
	if err != nil {
		return nil, err
	}
	temporary := New()
	temporary.Trees.treeDir = dir
	temporary.Trees.broker = c.broker
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() { cleanupErr = errors.Join(temporary.Drain(context.WithoutCancel(ctx)), os.RemoveAll(dir)) })
		return cleanupErr
	}
	store, err := temporary.Trees.indexStore(ctx, projectID, root)
	if err != nil {
		_ = cleanup()
		return nil, err
	}
	store.policy = c.policyFor(ctx, root.Path)
	store.policy.includeDependencies = true
	store.policy.selectedPaths = append([]string(nil), selectedPaths...)
	if err = store.reconcile(ctx, repochange.CurrentEpoch(root.Path)); err != nil {
		_ = cleanup()
		return nil, err
	}
	database, transaction, status, err := store.readTx(ctx, store.status)
	if err != nil {
		_ = cleanup()
		return nil, err
	}
	return &IndexReader{db: database, tx: transaction, Status: status, store: store, cleanup: cleanup}, nil
}

// BoundaryPath identifies lazy indexing policy without changing read admission.
func (c *Catalog) BoundaryPath(ctx context.Context, root, rel string, isDir bool) string {
	return c.Trees.policyFor(ctx, root).boundaryPath(rel, isDir)
}

// ObserveDependencyScope walks an explicitly named lazy subtree afresh without retaining it.
func (c *Catalog) ObserveDependencyScope(ctx context.Context, root Root) (Snapshot, error) {
	roots, err := cleanRoots([]Root{root})
	if err != nil {
		return Snapshot{}, err
	}
	root = roots[0]
	within := root.Path
	if root.Within != "" {
		within = root.Within
	}
	base, err := filepath.Rel(within, root.Path)
	if err != nil || base == ".." || hasParentPrefix(base) {
		return Snapshot{}, os.ErrPermission
	}
	policy := c.Trees.policyFor(ctx, within).under(filepath.ToSlash(base))
	policy.includeDependencies = true
	return buildSnapshot(ctx, roots, policy)
}
