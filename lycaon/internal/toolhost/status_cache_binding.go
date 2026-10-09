package toolhost

import (
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/git"
)

// statusCacheBinding serves the Git status cache native git_status reads. The
// runtime's default cache observes repository changes until a host cache
// replaces it; replacement releases that observation.
type statusCacheBinding struct {
	cache        atomic.Pointer[git.StatusCache]
	releaseOwned func()
}

func newStatusCacheBinding(owned *git.StatusCache) *statusCacheBinding {
	b := &statusCacheBinding{releaseOwned: owned.RegisterRepochangeObserver()}
	b.cache.Store(owned)
	return b
}

// Load returns the cache git_status reads.
func (b *statusCacheBinding) Load() *git.StatusCache {
	return b.cache.Load()
}

// replace serves cache and releases the default cache once it is replaced;
// repeated releases are no-ops.
func (b *statusCacheBinding) replace(cache *git.StatusCache) {
	if previous := b.cache.Swap(cache); previous != cache {
		b.releaseOwned()
	}
}
