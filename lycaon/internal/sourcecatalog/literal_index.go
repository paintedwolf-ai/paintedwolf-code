package sourcecatalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/litprefilter"
)

const (
	literalIndexCacheCap = 8
	literalBloomMinWords = 64
	literalBloomMaxWords = 32 * 1024
	literalIndexByteCap  = 128 << 20
	literalFileByteCap   = 192 << 20
	literalBuildWorkers  = 8
	// literalDirtyPrefixCap coalesces larger write bursts into a whole-root invalidation.
	literalDirtyPrefixCap = 256
	// literalIndexAdmissionWait is how long a build yields to background
	// holders of the IO and CPU lanes before reading on its own.
	literalIndexAdmissionWait = 300 * time.Millisecond
)

// LiteralQuery identifies one exact content-search scope within a generation
// the caller has already chosen.
type LiteralQuery struct {
	// FileScope selects index eligibility independently of content bloom facts.
	FileScope FileScope
	RootID    string
	Base      string
	// Require is matched case-insensitively under CanonicalFold, so the
	// candidate set is a superset for both case-sensitive and folded searches.
	Require litprefilter.Requirement
	// IncludeKey identifies the Include projection used to share indexes.
	IncludeKey string
	// Include filters entries before content is opened.
	Include func(Entry) bool
	// Open enforces containment for the query root.
	Open func(Entry) (io.ReadCloser, error)
}

type literalIndexedFile struct {
	entry Entry
	bloom literalBloom
	// unread marks a file the build could not open (permission denied); it
	// stays a candidate for every literal so the caller's open reports it.
	unread bool
}

type literalScopeIndex struct {
	files []literalIndexedFile
	bytes int64
}

var errLiteralIndexBudget = errors.New("literal index exceeds memory budget")

type literalIndexRecord struct {
	cancel   context.CancelFunc
	waiters  int
	rootPath string
	done     chan struct{}
	index    *literalScopeIndex
	err      error
	// dirty marks written prefixes as candidates until their blooms rebuild; "." covers the root.
	dirty []string
}

type literalIndexCache struct {
	mu      sync.Mutex
	records map[string]*literalIndexRecord
	order   []string
	bytes   int64

	fileMu    sync.Mutex
	files     literalFileCache
	fileReads map[*literalFileRead]struct{}
}

type literalFileRead struct {
	rootPath, path string
	invalidated    bool
}

type literalFileCacheEntry struct {
	size       int64
	mode       uint32
	modifiedNS int64
	searchable bool
	bloom      literalBloom
}

func newLiteralIndexCache() *literalIndexCache {
	return &literalIndexCache{
		records:   make(map[string]*literalIndexRecord),
		fileReads: make(map[*literalFileRead]struct{}),
	}
}

// invalidate drops observations affected by known writes, including reads in
// flight. Unknown-path events invalidate the whole root's content observations.
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
	c.mu.Lock()
	for _, record := range c.records {
		if record.rootPath == rootPath {
			record.dirty = appendDirtyPrefixes(record.dirty, prefixes)
		}
	}
	c.mu.Unlock()
	c.fileMu.Lock()
	for read := range c.fileReads {
		if read.rootPath == rootPath && entryUnderAnyBase(read.path, prefixes) {
			read.invalidated = true
		}
	}
	for _, prefix := range prefixes {
		c.files.drop(rootPath, prefix)
	}
	c.fileMu.Unlock()
}

func appendDirtyPrefixes(dirty, prefixes []string) []string {
	if len(dirty) == 1 && dirty[0] == "." {
		return dirty
	}
	if len(prefixes) == 0 {
		return []string{"."}
	}
	for _, prefix := range prefixes {
		if !entryUnderAnyBase(prefix, dirty) {
			dirty = append(dirty, prefix)
		}
	}
	if len(dirty) > literalDirtyPrefixCap {
		return []string{"."}
	}
	return dirty
}

// dirtyPaths distinguishes clean records from evicted indexes whose invalidations are no longer tracked.
func (c *literalIndexCache) dirtyPaths(key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record := c.records[key]
	if record == nil {
		return nil, false
	}
	return append([]string(nil), record.dirty...), true
}

func (c *literalIndexCache) dropRecordLocked(key string) {
	record := c.records[key]
	if record == nil {
		return
	}
	delete(c.records, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			if record.index != nil {
				c.bytes -= record.index.bytes
			}
			break
		}
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

// LiteralCandidates returns a conservative candidate set for the caller's pinned generation.
func (c *LiteralSearch) LiteralCandidates(ctx context.Context, snapshot Snapshot, query LiteralQuery) ([]Entry, error) {
	if c == nil {
		return nil, errors.New("source catalog is nil")
	}
	if query.Require.Empty() {
		return nil, nil
	}
	if snapshot.State != StateReady {
		return nil, errors.New("literal query needs a ready generation")
	}
	base := normalizeDir(query.Base)
	if query.Open == nil {
		return nil, errors.New("literal query opener is required")
	}
	if query.Include != nil && strings.TrimSpace(query.IncludeKey) == "" {
		return nil, errors.New("literal query include key is required with an inclusion policy")
	}
	rootPath := cleanAbs(snapshotRootPath(snapshot, query.RootID))
	if rootPath == "" {
		return nil, errors.New("literal index root is not in the source generation")
	}
	key := literalScopeKey(snapshot.Revision, query.RootID, base, query.IncludeKey)
	admit := func(buildCtx context.Context) (func(), error) {
		waitStart := time.Now()
		release, err := c.broker.Acquire(buildCtx, backgroundwork.Request{
			Key: key, Priority: backgroundwork.PriorityInteractive, Lane: query.RootID,
			Resources: []backgroundwork.Resource{backgroundwork.ResourceIO, backgroundwork.ResourceCPU},
			MaxWait:   literalIndexAdmissionWait,
		})
		if errors.Is(err, backgroundwork.ErrAcquireTimeout) {
			// The index answers a live search: it yields to the lanes' holders
			// for one interval, then reads unadmitted.
			slog.InfoContext(buildCtx, "literal index build proceeds without admission",
				"root", query.RootID, "waited_ms", time.Since(waitStart).Milliseconds())
			return func() {}, nil
		}
		return release, err
	}
	index, loadErr := c.cache.load(ctx, key, rootPath, func(buildCtx context.Context) (*literalScopeIndex, error) {
		return c.cache.buildScope(buildCtx, snapshot, rootPath, query, base, admit)
	})
	if errors.Is(loadErr, errLiteralIndexBudget) {
		return literalScopeEntries(ctx, snapshot, query, base)
	}
	if loadErr != nil {
		return nil, loadErr
	}
	folded := foldRequirement(query.Require)
	dirty, tracked := c.cache.dirtyPaths(key)
	if !tracked {
		return literalScopeEntries(ctx, snapshot, query, base)
	}
	out := make([]Entry, 0, len(index.files))
	for _, file := range index.files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.unread || entryUnderAnyBase(file.entry.Path, dirty) || file.bloom.admits(folded) {
			out = append(out, file.entry)
		}
	}
	return out, nil
}

func snapshotRootPath(snapshot Snapshot, rootID string) string {
	for _, root := range snapshot.Roots {
		if root.ID == rootID {
			return root.Path
		}
	}
	return ""
}

func (c *literalIndexCache) load(
	ctx context.Context,
	key, rootPath string,
	build func(context.Context) (*literalScopeIndex, error),
) (*literalScopeIndex, error) {
	c.mu.Lock()
	if existing := c.records[key]; existing != nil {
		existing.waiters++
		c.mu.Unlock()
		return c.wait(ctx, key, existing)
	}

	buildCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	record := &literalIndexRecord{rootPath: rootPath, done: make(chan struct{}), cancel: cancel, waiters: 1}
	c.records[key] = record
	c.mu.Unlock()

	go func() {
		defer cancel()
		record.index, record.err = build(buildCtx)
		c.mu.Lock()
		switch {
		case record.err != nil:
			if c.records[key] == record {
				delete(c.records, key)
			}
		case c.records[key] != record:
			// Evicted mid-build; the result still answers the waiters.
		default:
			c.order = append(c.order, key)
			c.bytes += record.index.bytes
			for len(c.order) > literalIndexCacheCap || c.bytes > literalIndexByteCap && len(c.order) > 0 {
				c.dropRecordLocked(c.order[0])
			}
		}
		close(record.done)
		c.mu.Unlock()
	}()

	return c.wait(ctx, key, record)
}

func (c *literalIndexCache) wait(ctx context.Context, key string, record *literalIndexRecord) (*literalScopeIndex, error) {
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		record.waiters--
		if record.waiters == 0 {
			select {
			case <-record.done:
			default:
				record.cancel()
				if c.records[key] == record {
					delete(c.records, key)
				}
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-record.done:
		return record.index, record.err
	}
}

type literalFileResult struct {
	bloom      literalBloom
	searchable bool
	err        error
}

// buildScope assembles one scope index. Cached blooms need no I/O and no
// admission; only the misses wait for the IO and CPU lanes.
func (c *literalIndexCache) buildScope(
	ctx context.Context,
	snapshot Snapshot,
	rootPath string,
	query LiteralQuery,
	base string,
	admit func(context.Context) (func(), error),
) (*literalScopeIndex, error) {
	var estimated int64
	for _, entry := range snapshot.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !literalScopeIncludes(entry, query, base) {
			continue
		}
		estimated += int64(literalBloomWords(entry.Size)*8 + literalEntryStringBytes(entry) + 256)
		if estimated > literalIndexByteCap {
			return nil, errLiteralIndexBudget
		}
	}
	entries, err := literalScopeEntries(ctx, snapshot, query, base)
	if err != nil {
		return nil, err
	}
	results := make([]literalFileResult, len(entries))
	var misses []int
	for i, entry := range entries {
		if bloom, searchable, ok := c.cachedBloom(rootPath, entry); ok {
			results[i] = literalFileResult{bloom: bloom, searchable: searchable}
			continue
		}
		misses = append(misses, i)
	}
	if len(misses) > 0 {
		release, err := admit(ctx)
		if err != nil {
			return nil, err
		}
		err = c.readBlooms(ctx, rootPath, entries, misses, results, query.Open)
		release()
		if err != nil {
			return nil, err
		}
	}

	index := &literalScopeIndex{files: make([]literalIndexedFile, 0, len(entries)), bytes: int64(len(entries) * 256)}
	for i, entry := range entries {
		result := results[i]
		if result.err != nil {
			if os.IsNotExist(result.err) || errors.Is(result.err, os.ErrPermission) {
				index.files = append(index.files, literalIndexedFile{entry: entry, unread: true})
				index.bytes += int64(literalEntryStringBytes(entry))
				continue
			}
			return nil, result.err
		}
		// Non-text files retain their metadata so later invalidation can make them candidates.
		bloom := result.bloom
		if !result.searchable {
			bloom = nil
		}
		index.files = append(index.files, literalIndexedFile{entry: entry, bloom: bloom})
		index.bytes += int64(len(bloom)*8 + literalEntryStringBytes(entry))
	}
	return index, nil
}

func (c *literalIndexCache) readBlooms(
	ctx context.Context,
	rootPath string,
	entries []Entry,
	misses []int,
	results []literalFileResult,
	open func(Entry) (io.ReadCloser, error),
) error {
	jobs := make(chan int)
	workers := min(len(misses), min(runtime.GOMAXPROCS(0), literalBuildWorkers))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				results[index].bloom, results[index].searchable, results[index].err =
					c.fileBloom(ctx, rootPath, entries[index], open)
			}
		}()
	}
	for _, index := range misses {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- index:
		}
	}
	close(jobs)
	wg.Wait()
	return nil
}

func (c *literalIndexCache) cachedBloom(rootPath string, entry Entry) (literalBloom, bool, bool) {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	cached, ok := c.files.lookup(rootPath, entry.Path)
	if !ok || cached.size != entry.Size || cached.mode != entry.Mode || cached.modifiedNS != entry.Modified.UnixNano() {
		return nil, false, false
	}
	return cached.bloom, cached.searchable, true
}

// fileBloom reads one file's bloom and caches it under the entry's size,
// mode, and mtime when a stat after the read still shows those values.
func (c *literalIndexCache) fileBloom(
	ctx context.Context,
	rootPath string,
	entry Entry,
	open func(Entry) (io.ReadCloser, error),
) (literalBloom, bool, error) {
	read := &literalFileRead{rootPath: rootPath, path: entry.Path}
	c.fileMu.Lock()
	c.fileReads[read] = struct{}{}
	c.fileMu.Unlock()
	defer func() { c.fileMu.Lock(); delete(c.fileReads, read); c.fileMu.Unlock() }()
	bloom, searchable, err := buildLiteralBloom(ctx, entry, open)
	if err != nil {
		return nil, false, err
	}
	if entryStillMatches(rootPath, entry) {
		c.fileMu.Lock()
		if !read.invalidated {
			c.files.store(rootPath, entry.Path, literalFileCacheEntry{
				size: entry.Size, mode: entry.Mode, modifiedNS: entry.Modified.UnixNano(),
				searchable: searchable, bloom: bloom,
			})
		}
		c.fileMu.Unlock()
	}
	return bloom, searchable, nil
}

// entryStillMatches reports whether the file on disk still carries the
// generation entry's size, mode, and mtime. A stat failure is a mismatch.
func entryStillMatches(rootPath string, entry Entry) bool {
	info, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(entry.Path)))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Size() == entry.Size && uint32(info.Mode()) == entry.Mode &&
		info.ModTime().UTC().UnixNano() == entry.Modified.UnixNano()
}

func literalScopeKey(revision uint64, rootID, base, includeKey string) string {
	return strings.Join([]string{rootID, base, includeKey, strconv.FormatUint(revision, 10)}, "\x00")
}

func entryUnderBase(entryPath, base string) bool {
	entryPath = normalizeDir(entryPath)
	base = normalizeDir(base)
	return base == "." || entryPath == base || strings.HasPrefix(entryPath, strings.TrimSuffix(base, "/")+"/")
}

func literalFileCost(keyBytes int, entry literalFileCacheEntry) int64 {
	return int64(keyBytes + len(entry.bloom)*8 + 128)
}

func literalScopeIncludes(entry Entry, query LiteralQuery, base string) bool {
	return entry.RootID == query.RootID && !entry.IsDir && !entry.IsSymlink && entryUnderBase(entry.Path, base) && (query.Include == nil || query.Include(entry))
}

func literalScopeEntries(ctx context.Context, snapshot Snapshot, query LiteralQuery, base string) ([]Entry, error) {
	var entries []Entry
	for _, entry := range snapshot.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if literalScopeIncludes(entry, query, base) {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func literalEntryStringBytes(entry Entry) int {
	return len(entry.Path) + len(entry.RootID) + len(entry.Parent) + len(entry.Name)
}

// CanPrune reports whether a cached Bloom filter proves entry cannot satisfy req.
func (c *LiteralSearch) CanPrune(rootPath string, req litprefilter.Requirement, entry Entry) bool {
	if c == nil || c.cache == nil || req.Empty() || entry.IsDir {
		return false
	}
	bloom, searchable, cached := c.cache.cachedBloom(rootPath, entry)
	if !cached || !searchable {
		return false
	}
	return !bloom.admits(foldRequirement(req))
}
