// Package sourcecatalog maintains the process-wide, rebuildable view of project trees.
package sourcecatalog

import (
	"context"
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

// ScopeProvider supplies walk budgets and traversal order without excluding paths.
type ScopeProvider interface {
	Catalog(ctx context.Context, root string) *sourcescope.Scope
}

// Catalog coalesces source scans and atomically publishes immutable snapshots.
type Catalog struct {
	structurePages      structurePageCache
	presentations       presentationStore
	navigationObservers navigationObservers
	trees               map[string]projectionStore
	treeDir             string
	treeLifecycle       sync.RWMutex
	mu                  sync.Mutex
	records             map[string]*record
	revision            uint64
	// rootLimit and scopedLimit bound each pool's record count.
	rootLimit   int
	scopedLimit int
	byteBudget  int64
	now         func() time.Time
	build       func(context.Context, []Root, walkPolicy) (Snapshot, error)
	broker      *backgroundwork.Broker
	literals    *literalIndexCache
	// scopesMu guards scopes on its own: policyFor runs under c.mu.
	scopesMu sync.RWMutex
	scopes   ScopeProvider
}

// SetScopes replaces bundled budgets and traversal priority with a provider.
func (c *Catalog) SetScopes(scopes ScopeProvider) {
	if c == nil || scopes == nil {
		return
	}
	c.scopesMu.Lock()
	c.scopes = scopes
	c.scopesMu.Unlock()
}

func New() *Catalog {
	return &Catalog{
		records:     make(map[string]*record),
		rootLimit:   defaultRootRecordLimit,
		scopedLimit: defaultScopedRecordLimit,
		byteBudget:  defaultByteBudget,
		now:         time.Now, build: buildSnapshot, broker: backgroundwork.Process(),
		literals: newLiteralIndexCache(),
	}
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
