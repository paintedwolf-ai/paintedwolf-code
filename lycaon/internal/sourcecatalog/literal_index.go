package sourcecatalog

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/litprefilter"
)

const (
	literalPreparationScopeCap = 8
	literalBloomMinWords       = 64
	literalBloomMaxWords       = 32 * 1024
	literalFileByteCap         = 192 << 20
	literalBuildWorkers        = 8
)

// LiteralQuery selects the content eligible for one paged preparation scope.
type LiteralQuery struct {
	FileScope  FileScope
	Require    litprefilter.Requirement
	IncludeKey string
	Include    func(Entry) bool
	Open       func(Entry) (io.ReadCloser, error)
}

type literalIndexCache struct {
	fileMu       sync.Mutex
	files        literalFileCache
	fileReads    map[*literalFileRead]struct{}
	workingBytes int64
	available    chan struct{}
	drained      bool
	versionOf    func(string, Entry) fspath.FileObservation
}

type literalFileRead struct {
	rootPath, path string
	invalidated    bool
	charge         int64
	version        fspath.FileObservation
}

type literalProjection struct {
	bloom      literalBloom
	searchable bool
}
type literalFileBlooms struct {
	decoded, raw literalProjection
	shared       bool
}

type literalFileCacheEntry struct {
	searchable    bool
	bloom         literalBloom
	rawBloom      literalBloom
	rawSearchable bool
	unavailable   bool
	shared        bool
	version       fspath.FileObservation
}

func newLiteralIndexCache() *literalIndexCache {
	return &literalIndexCache{fileReads: make(map[*literalFileRead]struct{}), available: make(chan struct{}), versionOf: fileVersion}
}

// invalidate fences active captures and releases only the written subtrees.
func (c *literalIndexCache) invalidate(rootPath string, paths []string) {
	if c == nil {
		return
	}
	rootPath = cleanAbs(rootPath)
	if rootPath == "" {
		return
	}
	prefixes, valid := reconciliationPaths(Root{Path: rootPath}, paths)
	if !valid || len(prefixes) == 0 {
		prefixes = []string{"."}
	}
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	for read := range c.fileReads {
		if read.rootPath == rootPath && entryUnderAnyBase(read.path, prefixes) {
			read.invalidated = true
		}
	}
	for _, prefix := range prefixes {
		c.files.drop(rootPath, prefix)
	}
}

func entryUnderAnyBase(entryPath string, bases []string) bool {
	for _, base := range bases {
		if entryUnderBase(entryPath, base) {
			return true
		}
	}
	return false
}

func entryUnderBase(entryPath, base string) bool {
	entryPath = normalizeDir(entryPath)
	base = normalizeDir(base)
	return base == "." || entryPath == base || strings.HasPrefix(entryPath, strings.TrimSuffix(base, "/")+"/")
}

// readBlooms keeps waiting producers separate from the bounded active builders.
func (c *literalIndexCache) readBlooms(ctx context.Context, rootPath string, entries []Entry, open func(Entry) (io.ReadCloser, error)) error {
	jobs := make(chan Entry)
	workers := min(len(entries), min(runtime.GOMAXPROCS(0), literalBuildWorkers))
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range jobs {
				err := c.fileBloom(ctx, rootPath, entry, open)
				if err != nil && !os.IsNotExist(err) && !errors.Is(err, os.ErrPermission) && !errors.Is(err, errLiteralReadIncomplete) {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
				}
			}
		}()
	}
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- entry:
		}
	}
	close(jobs)
	wg.Wait()
	return firstErr
}

func (c *literalIndexCache) cachedBloom(rootPath string, entry Entry) (literalBloom, bool, bool) {
	bloom, searchable, cached, _ := c.probeBloom(rootPath, entry)
	return bloom, searchable, cached
}

func (c *literalIndexCache) probeBloom(rootPath string, entry Entry) (literalBloom, bool, bool, bool) {
	version := c.versionOf(rootPath, entry)
	c.fileMu.Lock()
	cached, found := c.files.lookup(rootPath, entry.Path)
	changed := found && cached.version != version
	if changed {
		c.files.store(rootPath, entry.Path, literalFileCacheEntry{version: version, unavailable: true})
	}
	c.fileMu.Unlock()

	if !version.Known || version.Size <= 0 || version.Links == 0 || !found || changed || cached.unavailable {
		return nil, false, false, changed
	}
	return cached.bloom, cached.searchable, true, false
}

func (c *literalIndexCache) fileBloom(ctx context.Context, rootPath string, entry Entry, open func(Entry) (io.ReadCloser, error)) error {
	var read *literalFileRead
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		version := c.versionOf(rootPath, entry)
		if !version.Known || version.Size <= 0 || version.Links == 0 {
			return nil
		}
		c.fileMu.Lock()
		available := c.available
		drained := c.drained
		cached, found := c.files.lookup(rootPath, entry.Path)
		c.fileMu.Unlock()
		if drained {
			return errLiteralDrained
		}
		if found && cached.version == version && !cached.unavailable {
			return nil
		}
		read = c.beginFileRead(rootPath, entry, version)
		if read != nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-available:
		}
	}
	defer c.closeFileRead(read)
	observed := entry
	observed.Size = read.version.Size
	facts, err := buildLiteralBloom(ctx, observed, func(Entry) (io.ReadCloser, error) { return open(entry) })
	if err != nil {
		if errors.Is(err, os.ErrPermission) && read.version == c.versionOf(rootPath, entry) {
			c.fileMu.Lock()
			if !read.invalidated {
				c.files.store(rootPath, entry.Path, literalFileCacheEntry{unavailable: true, version: read.version})
			}
			c.fileMu.Unlock()
		}
		return err
	}
	if read.version == c.versionOf(rootPath, entry) {
		c.fileMu.Lock()
		if !read.invalidated {
			c.storeFile(rootPath, entry, facts, read.version)
		}
		c.fileMu.Unlock()
	}
	return nil
}

func literalFileCost(keyBytes, levels int, entry literalFileCacheEntry) int64 {
	words := len(entry.bloom) + len(entry.rawBloom)
	if entry.shared {
		words = len(entry.rawBloom)
	}
	return int64(keyBytes + words*8 + 256 + 192*levels)
}

// CanPrune consults the same live-version proof used by paged search.
func (c *LiteralSearch) CanPrune(rootPath string, req litprefilter.Requirement, entry Entry) bool {
	if c == nil || c.cache == nil || req.Empty() || entry.IsDir {
		return false
	}
	bloom, searchable, cached := c.cache.cachedBloom(rootPath, entry)
	return cached && searchable && !bloom.admitsRequirement(req)
}
