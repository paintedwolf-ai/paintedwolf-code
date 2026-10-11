package sourcecatalog

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
)

// LiteralSearch owns content bloom preparation and its retained candidate cache.
type LiteralSearch struct {
	cache  *literalIndexCache
	broker *backgroundwork.Broker
}

// contentBuild fills per-file literal filters to reduce content reads.
type contentBuild struct {
	revision uint64
	started  time.Time
	cancel   context.CancelFunc
	done     chan struct{}
	complete bool
	finished time.Time
}

type LiteralPage struct {
	Candidates  []Entry
	Warming     bool
	CachedFiles int
}

// IndexLiteralCandidates probes a bounded page. Missing observations remain
// candidates while one independent scan prepares reusable per-file blooms.
func (c *LiteralSearch) IndexLiteralCandidates(ctx context.Context, reader *IndexReader, query LiteralQuery, entries []Entry) LiteralPage {
	result := LiteralPage{Candidates: make([]Entry, 0, len(entries))}
	if ctx.Err() != nil || query.Open == nil || query.IncludeKey == "" {
		result.Candidates = entries
		return result
	}
	result.Warming = c.prepareIndexLiterals(ctx, reader, query)
	folded := foldRequirement(query.Require)

	for _, entry := range entries {
		bloom, searchable, cached := c.cache.cachedBloom(reader.store.root.Path, entry)
		if cached {
			result.CachedFiles++
		}
		if !cached || searchable && bloom.admits(folded) {
			result.Candidates = append(result.Candidates, entry)
		}
	}
	return result
}

func (c *LiteralSearch) prepareIndexLiterals(ctx context.Context, reader *IndexReader, query LiteralQuery) bool {
	s := reader.store
	key := indexLiteralScopeKey(query)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired {
		return false
	}
	if s.content == nil {
		s.content = map[string]*contentBuild{}
	}
	if build := s.content[key]; build != nil {
		select {
		case <-build.done:
			if build.revision == reader.Status.Revision && build.complete {
				return false
			}
			if !build.complete && time.Since(build.finished) < 5*time.Second {
				return true
			}
		default:
			return true
		}
	}
	if len(s.content) >= literalIndexCacheCap {
		var oldest string
		for key, build := range s.content {
			select {
			case <-build.done:
				if oldest == "" || build.started.Before(s.content[oldest].started) {
					oldest = key
				}
			default:
			}
		}
		if oldest == "" {
			return true
		}
		s.content[oldest].cancel()
		delete(s.content, oldest)
	}
	// Preparation outlives the search that started it; retirement and drain
	// cancel it.
	buildCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	build := &contentBuild{revision: reader.Status.Revision, started: time.Now(), cancel: cancel, done: make(chan struct{})}
	s.content[key] = build
	go func() {
		defer cancel()
		complete := c.buildIndexLiterals(buildCtx, s, query)
		s.mu.Lock()
		build.complete = complete
		build.finished = time.Now()
		close(build.done)
		s.mu.Unlock()
	}()
	return true
}

// buildIndexLiterals releases metadata transactions before content I/O and
// yields between pages.
func (c *LiteralSearch) buildIndexLiterals(ctx context.Context, s *indexStore, query LiteralQuery) bool {
	after := ""
	for ctx.Err() == nil {
		db, tx, status, err := s.readTx(ctx, TreeStatus{})
		if err != nil {
			return false
		}
		r := &IndexReader{db: db, tx: tx, Status: status, store: s}
		entries, err := r.FilePage(ctx, query.FileScope, after, TreeFilePageLimit)
		_ = r.Close()
		if err != nil {
			return false
		}
		if len(entries) == 0 {
			// A generation still filling in will have more files later; the
			// caller retries against the newer revision.
			return status.Complete
		}
		after = entries[len(entries)-1].Path
		var misses []Entry
		for _, entry := range entries {
			if query.Include != nil && !query.Include(entry) {
				continue
			}
			if _, _, cached := c.cache.cachedBloom(s.root.Path, entry); !cached {
				misses = append(misses, entry)
			}
		}
		if len(misses) == 0 {
			continue
		}
		release, err := c.broker.Acquire(ctx, backgroundwork.Request{
			Key: s.projectID + ":" + s.root.ID + ":content:" + query.IncludeKey, Lane: s.root.Path,
			Priority: backgroundwork.PriorityProactive, Resources: []backgroundwork.Resource{backgroundwork.ResourceIO, backgroundwork.ResourceCPU},
		})
		if err != nil {
			return false
		}
		indices := make([]int, len(misses))
		for i := range indices {
			indices[i] = i
		}
		results := make([]literalFileResult, len(misses))
		err = c.cache.readBlooms(ctx, s.root.Path, misses, indices, results, query.Open)
		release()
		if err != nil {
			return false
		}
	}
	return false
}

func indexLiteralScopeKey(query LiteralQuery) string {
	return query.FileScope.countColumn() + "\x00" + query.IncludeKey
}
