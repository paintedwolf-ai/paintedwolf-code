package sourcecatalog

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/textfile"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
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

type literalBloom []uint64

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
func (c *Catalog) LiteralCandidates(ctx context.Context, snapshot Snapshot, query LiteralQuery) ([]Entry, error) {
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
	index, loadErr := c.literals.load(ctx, key, rootPath, func(buildCtx context.Context) (*literalScopeIndex, error) {
		return c.literals.buildScope(buildCtx, snapshot, rootPath, query, base, admit)
	})
	if errors.Is(loadErr, errLiteralIndexBudget) {
		return literalScopeEntries(ctx, snapshot, query, base)
	}
	if loadErr != nil {
		return nil, loadErr
	}
	folded := foldRequirement(query.Require)
	dirty, tracked := c.literals.dirtyPaths(key)
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

func buildLiteralBloom(
	ctx context.Context,
	entry Entry,
	open func(Entry) (io.ReadCloser, error),
) (literalBloom, bool, error) {
	f, err := open(entry)
	if err != nil {
		return literalBloom{}, false, err
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReaderSize(f, 64*1024)
	prefix, _ := reader.Peek(3)
	var decoded io.Reader = reader
	if len(prefix) >= 2 && prefix[0] == 0xff && prefix[1] == 0xfe {
		decoded = transform.NewReader(reader, unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder())
	} else if len(prefix) >= 2 && prefix[0] == 0xfe && prefix[1] == 0xff {
		decoded = transform.NewReader(reader, unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder())
	} else if len(prefix) >= 3 && prefix[0] == 0xef && prefix[1] == 0xbb && prefix[2] == 0xbf {
		_, _ = reader.Discard(3)
	}
	return buildLiteralBloomReader(ctx, decoded, entry.Size)
}

// buildLiteralBloomReader carries split UTF-8 runes across chunks before folding.
func buildLiteralBloomReader(ctx context.Context, reader io.Reader, contentBytes int64) (literalBloom, bool, error) {
	bloom := newLiteralBloom(contentBytes)
	validator := textfile.UTF8Validator{}
	window := [3]byte{}
	filled := 0
	buf := make([]byte, 64*1024)
	folded := make([]byte, 0, 64*1024+8)
	var carry []byte
	for {
		if err := ctx.Err(); err != nil {
			return literalBloom{}, false, err
		}
		n, readErr := reader.Read(buf)
		atEOF := errors.Is(readErr, io.EOF)
		if !validator.Add(buf[:n], atEOF) {
			return literalBloom{}, false, nil
		}
		chunk := buf[:n]
		if len(carry) > 0 {
			chunk = append(carry, chunk...)
		}
		complete := chunk
		if !atEOF {
			complete = trimIncompleteUTF8Tail(chunk)
		}
		folded = litprefilter.AppendCanonicalFold(folded[:0], complete)
		carry = append(carry[:0], chunk[len(complete):]...)
		for _, b := range folded {
			if filled < len(window) {
				window[filled] = b
				filled++
				if filled < len(window) {
					continue
				}
			} else {
				window[0], window[1], window[2] = window[1], window[2], b
			}
			bloom.add(window)
		}
		if atEOF {
			return bloom, true, nil
		}
		if readErr != nil {
			return literalBloom{}, false, readErr
		}
	}
}

// trimIncompleteUTF8Tail drops up to three trailing bytes of a codepoint cut
// by a read boundary.
func trimIncompleteUTF8Tail(raw []byte) []byte {
	for removed := 0; removed < utf8.UTFMax-1 && len(raw) > 0; removed++ {
		if r, size := utf8.DecodeLastRune(raw); r != utf8.RuneError || size > 1 {
			break
		}
		raw = raw[:len(raw)-1]
	}
	return raw
}

func literalBloomWords(contentBytes int64) int {
	words := literalBloomMinWords
	target := int(contentBytes / 64) // one Bloom bit per content byte
	for words < target && words < literalBloomMaxWords {
		words *= 2
	}
	if words > literalBloomMaxWords {
		words = literalBloomMaxWords
	}
	return words
}

func (b *literalBloom) add(gram [3]byte) {
	if len(*b) == 0 {
		return
	}
	h := literalGramHash(gram)
	for _, shift := range [...]uint{0, 21, 42} {
		bit := (h >> shift) & literalBloomMask(len(*b))
		(*b)[bit/64] |= uint64(1) << (bit % 64)
	}
}

// foldedRequirement is a requirement in the bloom's canonical fold.
type foldedRequirement [][][]byte

func foldRequirement(req litprefilter.Requirement) foldedRequirement {
	out := make(foldedRequirement, 0, len(req.Clauses))
	for _, clause := range req.Clauses {
		folded := make([][]byte, 0, len(clause))
		for _, lit := range clause {
			folded = append(folded, litprefilter.AppendCanonicalFold(nil, lit.Bytes))
		}
		out = append(out, folded)
	}
	return out
}

// admits reports whether the file may satisfy every clause: a clause rules the
// file out only when the bloom excludes each of its literals.
func (b literalBloom) admits(req foldedRequirement) bool {
	for _, clause := range req {
		if !slices.ContainsFunc(clause, b.mayContain) {
			return false
		}
	}
	return true
}

func (b literalBloom) mayContain(folded []byte) bool {
	if len(b) == 0 {
		return false
	}
	if len(folded) < 3 {
		return true
	}
	for i := 0; i+2 < len(folded); i++ {
		gram := [3]byte{folded[i], folded[i+1], folded[i+2]}
		h := literalGramHash(gram)
		for _, shift := range [...]uint{0, 21, 42} {
			bit := (h >> shift) & literalBloomMask(len(b))
			if b[bit/64]&(uint64(1)<<(bit%64)) == 0 {
				return false
			}
		}
	}
	return true
}

func literalBloomMask(words int) uint64 {
	return uint64(words*64 - 1) //nolint:gosec // Bloom length is capped.
}

func literalGramHash(gram [3]byte) uint64 {
	x := uint64(gram[0])<<16 | uint64(gram[1])<<8 | uint64(gram[2])
	x ^= x >> 13
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	return x ^ (x >> 33)
}

func literalScopeKey(revision uint64, rootID, base, includeKey string) string {
	return strings.Join([]string{rootID, base, includeKey, strconv.FormatUint(revision, 10)}, "\x00")
}

func entryUnderBase(entryPath, base string) bool {
	entryPath = normalizeDir(entryPath)
	base = normalizeDir(base)
	return base == "." || entryPath == base || strings.HasPrefix(entryPath, strings.TrimSuffix(base, "/")+"/")
}

func newLiteralBloom(contentBytes int64) literalBloom {
	return make(literalBloom, literalBloomWords(contentBytes))
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
func (c *Catalog) CanPrune(rootPath string, req litprefilter.Requirement, entry Entry) bool {
	if c == nil || c.literals == nil || req.Empty() || entry.IsDir {
		return false
	}
	bloom, searchable, cached := c.literals.cachedBloom(rootPath, entry)
	if !cached || !searchable {
		return false
	}
	return !bloom.admits(foldRequirement(req))
}
