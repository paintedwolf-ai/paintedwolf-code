package sourcecatalog

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/repochange"
)

// ContentRead fences one live capture against source invalidation.
type ContentRead struct {
	stateMu sync.Mutex
	cache   *literalIndexCache
	read    *literalFileRead
	entry   Entry
	learned bool
	started bool
	stream  *contentReadStream
}

// BeginRead starts before the caller captures bytes through its read authority.
func (c *LiteralSearch) BeginRead(rootPath string, entry Entry, version fspath.FileObservation) *ContentRead {
	if c == nil || c.cache == nil || entry.IsDir || entry.IsSymlink || !version.Known || version.Links != 1 {
		return nil
	}
	rootPath = cleanAbs(rootPath)
	if rootPath == "" || entry.Path == "" {
		return nil
	}
	watch := c.cache.watchDirectory(rootPath, path.Dir(entry.Path))
	if !watch.Valid() {
		return nil
	}
	read := c.cache.beginFileRead(rootPath, entry, watch, version)
	if read == nil {
		return nil
	}
	return &ContentRead{cache: c.cache, read: read, entry: entry}
}

// Close releases an observation when capture fails or finishes.
func (r *ContentRead) Close() {
	if r == nil {
		return
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.closeLocked()
}

func (r *ContentRead) closeLocked() {
	if r.stream != nil {
		r.stream.builder = nil
		r.stream.finished = true
	}
	r.cache.closeFileRead(r.read)

}

// Observe retains bounded filters without reopening the captured file.
func (r *ContentRead) Observe(ctx context.Context, content []byte) bool {
	if r == nil {
		return false
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if !r.claim() {
		return false
	}
	defer r.closeLocked()
	if int64(len(content)) != r.entry.Size {
		return false
	}
	facts, err := buildLiteralBloom(ctx, r.entry, func(Entry) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(content)), nil })
	if err != nil {
		return false
	}
	return r.publish(ctx, facts)
}

func (r *ContentRead) publish(ctx context.Context, facts literalFileBlooms) bool {
	if ctx.Err() != nil || !r.read.watch.Valid() || r.read.watch != r.cache.watchDirectory(r.read.rootPath, path.Dir(r.entry.Path)) || !entryStillMatches(r.read.rootPath, r.entry) || r.read.version != fileVersion(r.read.rootPath, r.entry) {
		return false
	}
	r.cache.fileMu.Lock()
	defer r.cache.fileMu.Unlock()
	if _, active := r.cache.fileReads[r.read]; !active || r.read.invalidated {
		return false
	}
	r.cache.storeFile(r.read.rootPath, r.entry, facts, r.read.watch, r.read.version)
	r.learned = true
	return true

}

// storeFile requires fileMu and charges both representations to one cache entry.
func (c *literalIndexCache) storeFile(rootPath string, entry Entry, facts literalFileBlooms, watch repochange.WatchObservation, version fspath.FileObservation) {
	c.files.store(rootPath, entry.Path, literalFileCacheEntry{size: entry.Size, mode: entry.Mode, modifiedNS: entry.Modified.UnixNano(), searchable: facts.decoded.searchable, bloom: facts.decoded.bloom, rawBloom: facts.raw.bloom, rawSearchable: facts.raw.searchable, watch: watch, version: version})
}

// CanPruneBytes uses the byte representation consumed by command searches.
func (c *LiteralSearch) CanPruneBytes(rootPath string, req litprefilter.Requirement, entry Entry, version fspath.FileObservation) bool {
	if c == nil || c.cache == nil || req.Empty() || entry.IsDir || !version.Known || version.Links != 1 {
		return false
	}
	cached, ok := c.cache.cachedVersion(rootPath, entry, version)
	if !ok || !cached.rawSearchable {
		return false
	}
	return !cached.rawBloom.admits(foldRequirement(req))
}

// Wrap observes only bytes the consumer actually reads from a pinned stream.
func (r *ContentRead) Wrap(ctx context.Context, reader io.Reader) io.Reader {
	if r == nil {
		return reader
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if !r.claim() {
		return reader
	}
	r.stream = &contentReadStream{read: r, ctx: ctx, reader: reader, builder: newLiteralContent(r.entry.Size)}
	return r.stream
}

// Learned reports whether a complete observation reached the shared cache.
func (r *ContentRead) Learned() bool {
	if r == nil {
		return false
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.learned
}

type contentReadStream struct {
	read     *ContentRead
	ctx      context.Context
	reader   io.Reader
	builder  *literalContent
	bytes    int64
	finished bool
}

func (s *contentReadStream) Read(p []byte) (int, error) {
	n, err := s.reader.Read(p)
	s.read.stateMu.Lock()
	defer s.read.stateMu.Unlock()
	if !s.finished {
		s.bytes += int64(n)
		_, buildErr := s.builder.Write(p[:n])
		if buildErr != nil || s.bytes > s.read.entry.Size || err != nil && (err != io.EOF || s.bytes != s.read.entry.Size) {
			s.finished = true
			s.read.closeLocked()
		}
		if !s.finished && s.bytes == s.read.entry.Size {
			s.finished = true
			if facts, finishErr := s.builder.finish(); finishErr == nil {
				s.read.publish(s.ctx, facts)
			}
			s.read.closeLocked()
		}
	}
	return n, err
}

func (c *literalIndexCache) cachedFile(rootPath string, entry Entry) (literalFileCacheEntry, bool) {
	return c.cachedVersion(rootPath, entry, fileVersion(rootPath, entry))
}

func (c *literalIndexCache) cachedVersion(rootPath string, entry Entry, version fspath.FileObservation) (literalFileCacheEntry, bool) {
	if !version.Known || version.Links != 1 {
		return literalFileCacheEntry{}, false
	}
	watch := c.watchDirectory(rootPath, path.Dir(entry.Path))
	if !watch.Valid() {
		return literalFileCacheEntry{}, false
	}
	c.fileMu.Lock()
	cached, ok := c.files.lookup(rootPath, entry.Path)
	c.fileMu.Unlock()
	return cached, ok && cached.size == entry.Size && cached.mode == entry.Mode && cached.modifiedNS == entry.Modified.UnixNano() && cached.watch == watch && cached.version == version
}

func (c *literalIndexCache) fileWatch(rootPath string, entry Entry) repochange.WatchObservation {
	cached, ok := c.cachedFile(rootPath, entry)
	if !ok {
		return repochange.WatchObservation{}
	}
	return cached.watch
}

func (r *ContentRead) claim() bool {
	r.cache.fileMu.Lock()
	defer r.cache.fileMu.Unlock()
	_, active := r.cache.fileReads[r.read]
	if !active || r.started {
		return false
	}
	r.started = true
	return true
}

func fileVersion(rootPath string, entry Entry) fspath.FileObservation {
	info, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(entry.Path)))
	if err != nil || !info.Mode().IsRegular() {
		return fspath.FileObservation{}
	}
	return fspath.ObserveFile(info)
}
