// Package watchfd bounds what the engine's filesystem watcher may hold of the
// process's file descriptors.
//
// Bounds are in descriptors because a kqueue directory watch holds one for the
// directory and one per entry. The watcher yields to the rest of the engine.
package watchfd

import "sync"

// MaxDirs caps how many directories one watcher will register. A tree past the
// cap keeps a partial watch; every consumer has a poll or TTL path for the rest.
const MaxDirs = 4096

// MaxDirEntries is the widest directory a watcher will register where width
// costs descriptors. Directory width is bimodal: a few generated-output
// directories hold most of a tree's entries, and a poll covers them well.
const MaxDirEntries = 64

// DirCeiling is the most one directory may cost. Where a watch costs one unit
// this equals Cost itself and nothing is skipped for width.
func DirCeiling() int { return Cost(MaxDirEntries) }

// Budget is a ceiling on the descriptors the watcher may hold across every
// tree it watches.
type Budget struct {
	mu        sync.Mutex
	remaining int
}

// NewBudget returns a budget of exactly total descriptors.
func NewBudget(total int) *Budget {
	if total < 0 {
		total = 0
	}
	return &Budget{remaining: total}
}

// NewProcessBudget returns the watcher's share of this process's descriptors,
// read from the live limit because Go raises RLIMIT_NOFILE at startup.
func NewProcessBudget() *Budget { return NewBudget(poolDescriptors()) }

// Take draws n descriptors, reporting whether the budget covered them.
func (b *Budget) Take(n int) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.remaining {
		return false
	}
	b.remaining -= n
	return true
}

// Give returns the descriptors a closed watcher released.
func (b *Budget) Give(n int) {
	if b == nil || n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.remaining += n
}

// Remaining reports what is left to spend.
func (b *Budget) Remaining() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remaining
}
