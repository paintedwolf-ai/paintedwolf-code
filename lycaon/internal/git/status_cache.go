package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/repochange"
	"golang.org/x/sync/singleflight"
)

// DefaultStatusCacheTTL bounds board and client git staleness.
const DefaultStatusCacheTTL = 2 * time.Second

const statusCacheRefreshTimeout = 30 * time.Second

// statusLoader is the porcelain + log surface StatusCache needs (Manager).
type statusLoader interface {
	Status(ctx context.Context, projectDir string) (*GitStatus, error)
	Log(ctx context.Context, projectDir string, opts GitLogOpts) ([]GitCommit, error)
}

type statusCacheEntry struct {
	status   *GitStatus
	paths    []string
	built    time.Time
	revision uint64
	stale    bool
}

type subjectCacheEntry struct {
	subjects []string
	built    time.Time
}

// CachedStatus is one GetOrLoad result.
type CachedStatus struct {
	Status       *GitStatus
	ChangedPaths []string
	CacheHit     bool
	BuiltAt      time.Time
	Revision     uint64
	// ChangeSignal is why the last Invalidate happened for this dir
	// (mutation|watcher|ttl|git_host), or empty on first load.
	ChangeSignal repochange.Source
}

// StatusCache amortizes Manager.Status / porcelain cost per absolute project dir.
type StatusCache struct {
	loader statusLoader
	ttl    time.Duration
	now    func() time.Time

	mu       sync.Mutex
	entries  map[string]*statusCacheEntry
	subjects map[string]*subjectCacheEntry
	// lastSignal records the Source of the most recent Invalidate for dir.
	// A TTL miss without Invalidate is recorded as SourceTTL on the miss path.
	lastSignal map[string]repochange.Source
	// refreshing holds the dirs a background GetOrRevalidate reload is running for.
	refreshing map[string]struct{}
	// generations prevents invalidated loads from publishing.
	generations map[string]uint64
	group       singleflight.Group
	revision    uint64
}

var errStatusLoadSuperseded = errors.New("git status load superseded by invalidation")

// StatusCacheOption configures a StatusCache at construction.
type StatusCacheOption func(*StatusCache)

// WithStatusCacheClock sets the TTL clock.
func WithStatusCacheClock(now func() time.Time) StatusCacheOption {
	return func(c *StatusCache) {
		if now != nil {
			c.now = now
		}
	}
}

// NewStatusCache wraps loader (usually *Manager) with TTL + singleflight.
func NewStatusCache(loader statusLoader, opts ...StatusCacheOption) *StatusCache {
	c := &StatusCache{
		loader:      loader,
		ttl:         DefaultStatusCacheTTL,
		now:         time.Now,
		entries:     make(map[string]*statusCacheEntry),
		subjects:    make(map[string]*subjectCacheEntry),
		lastSignal:  make(map[string]repochange.Source),
		refreshing:  make(map[string]struct{}),
		generations: make(map[string]uint64),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// RegisterRepochangeObserver invalidates when refs, index, or working files change.
func (c *StatusCache) RegisterRepochangeObserver() {
	if c == nil {
		return
	}
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.Kind != repochange.HeadMoved && ev.Kind != repochange.WorktreeChanged && ev.Kind != repochange.IndexChanged {
			return
		}
		src := ev.Source
		if src == "" {
			src = repochange.SourceGitHost
		}
		c.InvalidateWithSource(ev.ProjectDir, src)
	})
}

// InvalidateWithSource marks status stale, clears subjects, and records the change source.
func (c *StatusCache) InvalidateWithSource(dir string, source repochange.Source) {
	if c == nil {
		return
	}
	key := cacheKey(dir)
	if key == "" {
		return
	}
	if source == "" {
		source = repochange.SourceGitHost
	}
	c.mu.Lock()
	// Revalidation leaves the last snapshot available.
	if ent := c.entries[key]; ent != nil {
		ent.stale = true
	}
	c.generations[key]++
	delete(c.subjects, key)
	c.lastSignal[key] = source
	c.mu.Unlock()
}

// LastChangeSignal returns the Source of the last Invalidate (or TTL miss marker).
func (c *StatusCache) LastChangeSignal(dir string) repochange.Source {
	if c == nil {
		return ""
	}
	key := cacheKey(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSignal[key]
}

// GetOrLoad returns cached Status when fresh; otherwise singleflight-loads.
// force=true skips TTL and any cached value (agent git_status path).
// Lazily starts the pruned worktree watcher after the first load.
func (c *StatusCache) GetOrLoad(ctx context.Context, dir string, force bool) (CachedStatus, error) {
	if c == nil || c.loader == nil {
		return CachedStatus{}, fmt.Errorf("git status cache not configured")
	}
	key := cacheKey(dir)
	if key == "" {
		st, err := c.loader.Status(ctx, dir)
		return CachedStatus{Status: st, ChangedPaths: changedPathsFromStatus(st)}, err
	}
	repochange.EnsureRoot(ctx, key)

	if !force {
		if hit, ok := c.lookup(key); ok {
			return hit, nil
		}
		// Expired entries report TTL; other misses retain the invalidation source.
		c.mu.Lock()
		if ent := c.entries[key]; ent != nil && c.now().Sub(ent.built) >= c.ttl {
			c.lastSignal[key] = repochange.SourceTTL
			ent.stale = true
		} else if _, had := c.lastSignal[key]; !had {
			c.lastSignal[key] = repochange.SourceTTL
		}
		c.mu.Unlock()
	}

	for {
		v, err, _ := c.group.Do(key, func() (any, error) {
			if !force {
				if hit, ok := c.lookup(key); ok {
					return hit, nil
				}
			}
			c.mu.Lock()
			generation := c.generations[key]
			c.mu.Unlock()
			st, err := c.loader.Status(ctx, key)
			if err != nil {
				return CachedStatus{}, err
			}
			built := c.now()
			paths := changedPathsFromStatus(st)
			c.mu.Lock()
			if c.generations[key] != generation {
				c.mu.Unlock()
				return CachedStatus{}, errStatusLoadSuperseded
			}
			c.revision++
			revision := c.revision
			entry := &statusCacheEntry{status: cloneStatus(st), paths: paths, built: built, revision: revision}
			c.entries[key] = entry
			signal := c.lastSignal[key]
			c.mu.Unlock()
			return CachedStatus{
				Status:       cloneStatus(st),
				ChangedPaths: append([]string(nil), paths...),
				CacheHit:     false,
				BuiltAt:      built,
				Revision:     revision,
				ChangeSignal: signal,
			}, nil
		})
		if errors.Is(err, errStatusLoadSuperseded) {
			if ctx.Err() != nil {
				return CachedStatus{}, ctx.Err()
			}
			continue
		}
		if err != nil {
			return CachedStatus{}, err
		}
		return v.(CachedStatus), nil
	}
}

// GetOrRevalidate serves the last snapshot while refreshing stale entries.
// A cold cache loads synchronously.
func (c *StatusCache) GetOrRevalidate(ctx context.Context, dir string) (CachedStatus, error) {
	if c == nil || c.loader == nil {
		return CachedStatus{}, fmt.Errorf("git status cache not configured")
	}
	key := cacheKey(dir)
	if key == "" {
		return c.GetOrLoad(ctx, dir, false)
	}
	cached, ok := c.lookupAny(key)
	if !ok {
		return c.GetOrLoad(ctx, dir, false)
	}
	if cached.CacheHit {
		return cached, nil
	}
	c.revalidate(ctx, key)
	return cached, nil
}

// PeekOrRevalidate returns immediately and refreshes absent or stale entries.
func (c *StatusCache) PeekOrRevalidate(ctx context.Context, dir string) (CachedStatus, bool, error) {
	if c == nil || c.loader == nil {
		return CachedStatus{}, false, fmt.Errorf("git status cache not configured")
	}
	key := cacheKey(dir)
	if key == "" {
		return CachedStatus{}, false, fmt.Errorf("git status cache requires an absolute project directory")
	}
	cached, ok := c.lookupAny(key)
	if !ok {
		c.revalidate(ctx, key)
		return CachedStatus{}, false, nil
	}
	if !cached.CacheHit {
		c.revalidate(ctx, key)
	}
	return cached, true, nil
}

// revalidate bounds one detached refresh per directory.
func (c *StatusCache) revalidate(ctx context.Context, key string) {
	c.mu.Lock()
	if _, busy := c.refreshing[key]; busy {
		c.mu.Unlock()
		return
	}
	c.refreshing[key] = struct{}{}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusCacheRefreshTimeout)
	go func() {
		defer cancel()
		defer observability.GuardPanic("git.status_cache.revalidate")
		defer func() {
			c.mu.Lock()
			delete(c.refreshing, key)
			c.mu.Unlock()
		}()
		_, _ = c.GetOrLoad(ctx, key, false)
	}()
}

// RecentSubjects returns recent commit subjects (separate from Status hot path).
// force skips the subject TTL cache. Invalidated with HeadMoved via Invalidate.
func (c *StatusCache) RecentSubjects(ctx context.Context, dir string, force bool, limit int) ([]string, error) {
	if c == nil || c.loader == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = gitStatusRecentCommitLimit
	}
	key := cacheKey(dir)
	if key == "" {
		return c.loadSubjects(ctx, dir, limit)
	}
	if !force {
		c.mu.Lock()
		ent := c.subjects[key]
		if ent != nil && c.now().Sub(ent.built) < c.ttl {
			out := append([]string(nil), ent.subjects...)
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()
	}
	subjects, err := c.loadSubjects(ctx, key, limit)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.subjects[key] = &subjectCacheEntry{subjects: append([]string(nil), subjects...), built: c.now()}
	c.mu.Unlock()
	return subjects, nil
}

func (c *StatusCache) loadSubjects(ctx context.Context, dir string, limit int) ([]string, error) {
	commits, err := c.loader.Log(ctx, dir, GitLogOpts{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(commits))
	for _, cm := range commits {
		out = append(out, cm.Subject)
	}
	return out, nil
}

func (c *StatusCache) lookup(key string) (CachedStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ent := c.entries[key]
	if ent == nil || ent.stale || c.now().Sub(ent.built) >= c.ttl {
		return CachedStatus{}, false
	}
	return c.snapshotLocked(key, ent, true), true
}

// lookupAny returns the last value for key regardless of freshness.
func (c *StatusCache) lookupAny(key string) (CachedStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ent := c.entries[key]
	if ent == nil {
		return CachedStatus{}, false
	}
	fresh := !ent.stale && c.now().Sub(ent.built) < c.ttl
	return c.snapshotLocked(key, ent, fresh), true
}

func (c *StatusCache) snapshotLocked(key string, ent *statusCacheEntry, hit bool) CachedStatus {
	return CachedStatus{
		Status:       cloneStatus(ent.status),
		ChangedPaths: append([]string(nil), ent.paths...),
		CacheHit:     hit,
		BuiltAt:      ent.built,
		Revision:     ent.revision,
		ChangeSignal: c.lastSignal[key],
	}
}

// Roots in one repository share status and invalidation; non-repository paths key themselves.
func cacheKey(dir string) string {
	if filepath.Clean(strings.TrimSpace(dir)) == "." {
		return ""
	}
	abs := gitrepo.CanonicalDir(dir)
	if abs == "" {
		return ""
	}
	if repo, ok := gitrepo.Discover(abs); ok {
		return repo.Root
	}
	return abs
}

func changedPathsFromStatus(st *GitStatus) []string {
	if st == nil || len(st.Files) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(st.Files))
	out := make([]string, 0, len(st.Files))
	for _, f := range st.Files {
		if f.Path == "" {
			continue
		}
		if _, ok := seen[f.Path]; ok {
			continue
		}
		seen[f.Path] = struct{}{}
		out = append(out, f.Path)
	}
	return out
}

func cloneStatus(st *GitStatus) *GitStatus {
	if st == nil {
		return nil
	}
	cp := *st
	if st.Files != nil {
		cp.Files = append([]GitStatusEntry(nil), st.Files...)
	}
	if st.RecentCommits != nil {
		cp.RecentCommits = append([]string(nil), st.RecentCommits...)
	}
	return &cp
}
