package sourcecatalog

import (
	"bytes"
	"context"
	"io"
	"path"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/repochange"
)

// ContentRead fences one live capture against source invalidation.
type ContentRead struct {
	cache   *literalIndexCache
	read    *literalFileRead
	entry   Entry
	learned bool
}

// BeginRead starts before the caller captures bytes through its read authority.
func (c *LiteralSearch) BeginRead(rootPath string, entry Entry) *ContentRead {
	if c == nil || c.cache == nil || entry.IsDir || entry.IsSymlink {
		return nil
	}
	rootPath = cleanAbs(rootPath)
	if rootPath == "" || entry.Path == "" {
		return nil
	}
	watch := repochange.ObserveWatch(rootPath, path.Dir(entry.Path))
	if !watch.Valid() {
		return nil
	}
	read := &literalFileRead{rootPath: rootPath, path: entry.Path, watch: watch}
	c.cache.fileMu.Lock()
	if cached, ok := c.cache.files.lookup(rootPath, entry.Path); ok && cached.size == entry.Size && cached.mode == entry.Mode && cached.modifiedNS == entry.Modified.UnixNano() && cached.watch == watch {
		c.cache.fileMu.Unlock()
		return nil
	}
	for active := range c.cache.fileReads {
		if active.rootPath == rootPath && active.path == entry.Path && !active.invalidated {
			c.cache.fileMu.Unlock()
			return nil
		}
	}
	c.cache.fileReads[read] = struct{}{}
	c.cache.fileMu.Unlock()
	return &ContentRead{cache: c.cache, read: read, entry: entry}
}

// Close releases an observation when capture fails or finishes.
func (r *ContentRead) Close() {
	if r == nil {
		return
	}
	r.cache.fileMu.Lock()
	delete(r.cache.fileReads, r.read)
	r.cache.fileMu.Unlock()
}

// Observe retains bounded filters without reopening the captured file.
func (r *ContentRead) Observe(ctx context.Context, content []byte) bool {
	if r == nil {
		return false
	}
	defer r.Close()
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
	if ctx.Err() != nil || !r.read.watch.Valid() || r.read.watch != repochange.ObserveWatch(r.read.rootPath, path.Dir(r.entry.Path)) || !entryStillMatches(r.read.rootPath, r.entry) {
		return false
	}
	r.cache.fileMu.Lock()
	defer r.cache.fileMu.Unlock()
	if _, active := r.cache.fileReads[r.read]; !active || r.read.invalidated {
		return false
	}
	r.cache.storeFile(r.read.rootPath, r.entry, facts, r.read.watch)
	r.learned = true
	return true

}

// storeFile requires fileMu and charges both representations to one cache entry.
func (c *literalIndexCache) storeFile(rootPath string, entry Entry, facts literalFileBlooms, watch repochange.WatchObservation) {
	c.files.store(rootPath, entry.Path, literalFileCacheEntry{size: entry.Size, mode: entry.Mode, modifiedNS: entry.Modified.UnixNano(), searchable: facts.decoded.searchable, bloom: facts.decoded.bloom, rawBloom: facts.raw.bloom, rawSearchable: facts.raw.searchable, watch: watch})
}

// CanPruneBytes uses the byte representation consumed by command searches.
func (c *LiteralSearch) CanPruneBytes(rootPath string, req litprefilter.Requirement, entry Entry) bool {
	if c == nil || c.cache == nil || req.Empty() || entry.IsDir {
		return false
	}
	cached, ok := c.cache.cachedFile(rootPath, entry)
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
	return &contentReadStream{read: r, ctx: ctx, reader: reader, builder: newLiteralContent(r.entry.Size)}
}

// Learned reports whether a complete observation reached the shared cache.
func (r *ContentRead) Learned() bool { return r != nil && r.learned }

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
	if !s.finished {
		s.bytes += int64(n)
		_, buildErr := s.builder.Write(p[:n])
		if buildErr != nil || s.bytes > s.read.entry.Size || err != nil && (err != io.EOF || s.bytes != s.read.entry.Size) {
			s.finished = true
			s.read.Close()
		}
		if !s.finished && s.bytes == s.read.entry.Size {
			s.finished = true
			if facts, finishErr := s.builder.finish(); finishErr == nil {
				s.read.publish(s.ctx, facts)
			}
			s.read.Close()
		}
	}
	return n, err
}

func (c *literalIndexCache) cachedFile(rootPath string, entry Entry) (literalFileCacheEntry, bool) {
	watch := repochange.ObserveWatch(rootPath, path.Dir(entry.Path))
	if !watch.Valid() {
		return literalFileCacheEntry{}, false
	}
	c.fileMu.Lock()
	cached, ok := c.files.lookup(rootPath, entry.Path)
	c.fileMu.Unlock()
	return cached, ok && cached.size == entry.Size && cached.mode == entry.Mode && cached.modifiedNS == entry.Modified.UnixNano() && cached.watch == watch
}

func (c *literalIndexCache) fileWatch(rootPath string, entry Entry) repochange.WatchObservation {
	cached, ok := c.cachedFile(rootPath, entry)
	if !ok {
		return repochange.WatchObservation{}
	}
	return cached.watch
}
