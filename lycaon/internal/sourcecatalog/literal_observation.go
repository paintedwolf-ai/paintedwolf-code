package sourcecatalog

import (
	"bytes"
	"context"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"io"
)

// ContentRead fences one live capture against source invalidation.
type ContentRead struct {
	cache *literalIndexCache
	read  *literalFileRead
	entry Entry
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
	read := &literalFileRead{rootPath: rootPath, path: entry.Path}
	c.cache.fileMu.Lock()
	if cached, ok := c.cache.files.lookup(rootPath, entry.Path); ok && cached.size == entry.Size && cached.mode == entry.Mode && cached.modifiedNS == entry.Modified.UnixNano() {
		c.cache.fileMu.Unlock()
		return nil
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
func (r *ContentRead) Observe(ctx context.Context, content []byte) {
	if r == nil || int64(len(content)) != r.entry.Size {
		return
	}
	defer r.Close()
	facts, err := buildLiteralBloom(ctx, r.entry, func(Entry) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(content)), nil })
	if err != nil || ctx.Err() != nil || !entryStillMatches(r.read.rootPath, r.entry) {
		return
	}
	r.cache.fileMu.Lock()
	defer r.cache.fileMu.Unlock()
	if _, active := r.cache.fileReads[r.read]; !active || r.read.invalidated {
		return
	}
	r.cache.storeFile(r.read.rootPath, r.entry, facts)
}

// storeFile requires fileMu and charges both representations to one cache entry.
func (c *literalIndexCache) storeFile(rootPath string, entry Entry, facts literalFileBlooms) {
	c.files.store(rootPath, entry.Path, literalFileCacheEntry{size: entry.Size, mode: entry.Mode, modifiedNS: entry.Modified.UnixNano(), searchable: facts.decoded.searchable, bloom: facts.decoded.bloom, rawBloom: facts.raw.bloom, rawSearchable: facts.raw.searchable})
}

// CanPruneBytes uses the byte representation consumed by command searches.
func (c *LiteralSearch) CanPruneBytes(rootPath string, req litprefilter.Requirement, entry Entry) bool {
	if c == nil || c.cache == nil || req.Empty() || entry.IsDir {
		return false
	}
	c.cache.fileMu.Lock()
	cached, ok := c.cache.files.lookup(rootPath, entry.Path)
	c.cache.fileMu.Unlock()
	if !ok || !cached.rawSearchable || cached.size != entry.Size || cached.mode != entry.Mode || cached.modifiedNS != entry.Modified.UnixNano() {
		return false
	}
	return !cached.rawBloom.admits(foldRequirement(req))
}
