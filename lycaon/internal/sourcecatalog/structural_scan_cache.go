package sourcecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

const maxStructuralScanCachedDescriptors = 512

var structuralScanCachedDescriptors atomic.Int64

type structuralScanDirectoryCache struct {
	mu      sync.Mutex
	limit   int
	root    *os.File
	entries map[string]*structuralScanDirectoryCacheEntry
	order   []string
}

type structuralScanDirectoryCacheEntry struct {
	file    *os.File
	users   int
	evicted bool
}

func newStructuralScanDirectoryCache(root *os.Root, limit int) *structuralScanDirectoryCache {
	cache := &structuralScanDirectoryCache{limit: limit, entries: make(map[string]*structuralScanDirectoryCacheEntry)}
	if limit <= 0 || !structuralScanDirectoryCacheSupported() || !reserveStructuralScanCachedDescriptor() {
		cache.limit = 0
		return cache
	}
	rootFile, err := openStructuralScanCacheRootFile(root)
	if err != nil {
		releaseStructuralScanCachedDescriptor()
		cache.limit = 0
		return cache
	}
	cache.root = rootFile
	return cache
}

func openStructuralScanCachedDirectory(dir string, cache *structuralScanDirectoryCache) (*os.File, error) {
	if cache == nil || cache.root == nil || dir == "." {
		return nil, nil
	}
	parent := normalizeDir(filepath.ToSlash(filepath.Dir(dir)))
	base := filepath.Base(dir)
	return cache.openChild(parent, base)
}

func (c *structuralScanDirectoryCache) openChild(parent, base string) (*os.File, error) {
	entry := c.acquire(parent)
	if entry == nil {
		return nil, nil
	}
	defer c.release(entry)
	file, err := openStructuralScanChildDirectoryFile(entry.file, base)
	if errors.Is(err, errStructuralScanDirectoryCacheUnsupported) {
		return nil, nil
	}
	if err != nil {
		c.remove(parent)
		return nil, nil
	}
	dir := normalizeDir(filepath.ToSlash(filepath.Join(parent, base)))
	current, err := structuralScanPathMatchesOpenDirectory(c.root, dir, file)
	if err != nil || !current {
		_ = file.Close()
		c.remove(parent)
		return nil, nil
	}
	return file, nil
}

func (c *structuralScanDirectoryCache) acquire(dir string) *structuralScanDirectoryCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[dir]
	if entry == nil || entry.evicted {
		return nil
	}
	entry.users++
	return entry
}

func (c *structuralScanDirectoryCache) release(entry *structuralScanDirectoryCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.users--
	if entry.users == 0 && entry.evicted {
		_ = entry.file.Close()
		releaseStructuralScanCachedDescriptor()
	}
}

func (c *structuralScanDirectoryCache) remember(dir string, file *os.File) {
	if c == nil || c.root == nil || c.limit <= 0 || file == nil {
		return
	}
	c.mu.Lock()
	if len(c.order) >= c.limit {
		c.evictOldestLocked()
	}
	c.mu.Unlock()
	if !reserveStructuralScanCachedDescriptor() {
		c.mu.Lock()
		c.evictOldestLocked()
		c.mu.Unlock()
		if !reserveStructuralScanCachedDescriptor() {
			return
		}
	}
	copyFile, err := duplicateStructuralScanDirectoryFile(file)
	if err != nil {
		releaseStructuralScanCachedDescriptor()
		return
	}
	entry := &structuralScanDirectoryCacheEntry{file: copyFile}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous := c.entries[dir]; previous != nil {
		c.evictLocked(dir, previous)
	} else {
		c.order = append(c.order, dir)
	}
	c.entries[dir] = entry
	for len(c.order) > c.limit {
		c.evictOldestLocked()
	}
}

func (c *structuralScanDirectoryCache) evictOldestLocked() {
	if len(c.order) == 0 {
		return
	}
	oldest := c.order[0]
	c.order = append(c.order[:0], c.order[1:]...)
	if entry := c.entries[oldest]; entry != nil {
		c.evictLocked(oldest, entry)
	}
}

func (c *structuralScanDirectoryCache) remove(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[dir]
	if entry == nil {
		return
	}
	c.evictLocked(dir, entry)
	for i, cached := range c.order {
		if cached == dir {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func (c *structuralScanDirectoryCache) evictLocked(dir string, entry *structuralScanDirectoryCacheEntry) {
	delete(c.entries, dir)
	if entry.evicted {
		return
	}
	entry.evicted = true
	if entry.users == 0 {
		_ = entry.file.Close()
		releaseStructuralScanCachedDescriptor()
	}
}

func (c *structuralScanDirectoryCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for dir, entry := range c.entries {
		c.evictLocked(dir, entry)
	}
	c.entries = nil
	c.order = nil
	if c.root != nil {
		_ = c.root.Close()
		c.root = nil
		releaseStructuralScanCachedDescriptor()
	}
}

func reserveStructuralScanCachedDescriptor() bool {
	for {
		used := structuralScanCachedDescriptors.Load()
		if used >= maxStructuralScanCachedDescriptors {
			return false
		}
		if structuralScanCachedDescriptors.CompareAndSwap(used, used+1) {
			return true
		}
	}
}

func releaseStructuralScanCachedDescriptor() { structuralScanCachedDescriptors.Add(-1) }
