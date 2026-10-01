package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/projectroot"
	"golang.org/x/sync/singleflight"
)

// RepoSetCache separates project and session root sets before applying generation and TTL.
type RepoSetCache struct {
	ttl      time.Duration
	now      func() time.Time
	discover func(context.Context, []projectroot.RootRef) []RepoRef

	mu         sync.Mutex
	entries    map[repoSetKey]*repoSetCacheEntry
	refreshing map[string]struct{}
	group      singleflight.Group
}

type repoSetKey struct {
	projectID   string
	rootsDigest string
}

func repositorySetKey(projectID string, roots []projectroot.RootRef) repoSetKey {
	hash := sha256.New()
	for _, root := range roots {
		_, _ = fmt.Fprintf(hash, "%q\x00%q\x00%q\x00%t\x00", root.ID, root.Path, root.Label, root.IsPrimary)
	}
	return repoSetKey{projectID: projectID, rootsDigest: hex.EncodeToString(hash.Sum(nil))}
}

func (key repoSetKey) flight(rootsGeneration int) string {
	return key.projectID + "\x00" + key.rootsDigest + "\x00" + strconv.Itoa(rootsGeneration)
}

type repoSetCacheEntry struct {
	generation int
	repos      []RepoRef
	built      time.Time
}

// RepoSetCacheOption configures a RepoSetCache at construction.
type RepoSetCacheOption func(*RepoSetCache)

// WithRepoSetCacheClock overrides the time source used for TTL freshness.
func WithRepoSetCacheClock(now func() time.Time) RepoSetCacheOption {
	return func(c *RepoSetCache) {
		if now != nil {
			c.now = now
		}
	}
}

// WithRepoSetDiscover supplies repository discovery.
func WithRepoSetDiscover(fn func(context.Context, []projectroot.RootRef) []RepoRef) RepoSetCacheOption {
	return func(c *RepoSetCache) {
		if fn != nil {
			c.discover = fn
		}
	}
}

// NewRepoSetCache uses DefaultStatusCacheTTL for non-positive durations.
func NewRepoSetCache(ttl time.Duration, opts ...RepoSetCacheOption) *RepoSetCache {
	if ttl <= 0 {
		ttl = DefaultStatusCacheTTL
	}
	c := &RepoSetCache{
		ttl:        ttl,
		now:        time.Now,
		discover:   DiscoverRepos,
		entries:    make(map[repoSetKey]*repoSetCacheEntry),
		refreshing: make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// PeekOrRevalidate returns cached repository sets and schedules discovery on misses.
func (c *RepoSetCache) PeekOrRevalidate(
	ctx context.Context,
	projectID string,
	rootsGeneration int,
	roots []projectroot.RootRef,
) ([]RepoRef, bool) {
	if c == nil {
		return nil, false
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, false
	}
	key := repositorySetKey(projectID, roots)
	if hit, ok := c.lookup(key, rootsGeneration); ok {
		return hit, true
	}
	if stale, ok := c.lookupGeneration(key, rootsGeneration); ok {
		c.revalidate(ctx, projectID, rootsGeneration, roots)
		return stale, true
	}
	c.revalidate(ctx, projectID, rootsGeneration, roots)
	return nil, false
}

func (c *RepoSetCache) revalidate(
	ctx context.Context,
	projectID string,
	rootsGeneration int,
	roots []projectroot.RootRef,
) {
	flightKey := repositorySetKey(projectID, roots).flight(rootsGeneration)
	c.mu.Lock()
	if _, busy := c.refreshing[flightKey]; busy {
		c.mu.Unlock()
		return
	}
	c.refreshing[flightKey] = struct{}{}
	c.mu.Unlock()

	rootCopy := append([]projectroot.RootRef(nil), roots...)
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	go func() {
		defer cancel()
		defer observability.GuardPanic("git.repo_set_cache.revalidate")
		defer func() {
			c.mu.Lock()
			delete(c.refreshing, flightKey)
			c.mu.Unlock()
		}()
		c.GetOrLoad(refreshCtx, projectID, rootsGeneration, rootCopy)
	}()
}

// GetOrLoad shares discovery when the generation or TTL invalidates the cache.
func (c *RepoSetCache) GetOrLoad(ctx context.Context, projectID string, rootsGeneration int, roots []projectroot.RootRef) []RepoRef {
	if c == nil {
		return DiscoverRepos(ctx, roots)
	}
	discover := c.discover
	if discover == nil {
		discover = DiscoverRepos
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return discover(ctx, roots)
	}

	key := repositorySetKey(projectID, roots)
	if hit, ok := c.lookup(key, rootsGeneration); ok {
		return hit
	}

	flightKey := repositorySetKey(projectID, roots).flight(rootsGeneration)
	v, _, _ := c.group.Do(flightKey, func() (any, error) {
		if hit, ok := c.lookup(key, rootsGeneration); ok {
			return hit, nil
		}
		repos := discover(ctx, roots)
		built := c.now()
		c.mu.Lock()
		c.entries[key] = &repoSetCacheEntry{
			generation: rootsGeneration,
			repos:      cloneRepoRefs(repos),
			built:      built,
		}
		c.mu.Unlock()
		return cloneRepoRefs(repos), nil
	})
	if v == nil {
		return nil
	}
	return v.([]RepoRef)
}

// Invalidate drops the cached set for projectID so the next GetOrLoad re-probes.
func (c *RepoSetCache) Invalidate(projectID string) {
	if c == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	c.mu.Lock()
	for key := range c.entries {
		if key.projectID == projectID {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

func (c *RepoSetCache) lookup(key repoSetKey, rootsGeneration int) ([]RepoRef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ent := c.entries[key]
	if ent == nil {
		return nil, false
	}
	if ent.generation != rootsGeneration {
		return nil, false
	}
	if c.now().Sub(ent.built) >= c.ttl {
		return nil, false
	}
	return cloneRepoRefs(ent.repos), true
}

func (c *RepoSetCache) lookupGeneration(key repoSetKey, rootsGeneration int) ([]RepoRef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ent := c.entries[key]
	if ent == nil || ent.generation != rootsGeneration {
		return nil, false
	}
	return cloneRepoRefs(ent.repos), true
}
