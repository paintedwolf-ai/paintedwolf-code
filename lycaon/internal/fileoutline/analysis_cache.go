package fileoutline

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/tsparse"
)

const (
	analysisCacheMaxEntries = 512
	analysisCacheByteBudget = 32 << 20
	analysisCacheKeyVersion = "1"
)

type analysisCacheEntry struct {
	key     string
	result  Result
	bytes   int64
	expires time.Time
}

type analysisFlight struct {
	done   chan struct{}
	result Result
}

type analysisCache struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      *list.List
	flights    map[string]*analysisFlight
	maxEntries int
	maxBytes   int64
	bytes      int64
	hits       int64
	misses     int64
	joins      int64
	evictions  int64
	bypasses   int64
}

// CacheStats reports process-wide reuse of immutable source analysis.
type CacheStats struct {
	Entries   int
	Bytes     int64
	Hits      int64
	Misses    int64
	Joins     int64
	Evictions int64
	Bypasses  int64
}

var processAnalysisCache = newAnalysisCache(analysisCacheMaxEntries, analysisCacheByteBudget)

func newAnalysisCache(maxEntries int, maxBytes int64) *analysisCache {
	if maxEntries < 1 {
		maxEntries = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &analysisCache{
		entries: make(map[string]*list.Element), order: list.New(), flights: make(map[string]*analysisFlight),
		maxEntries: maxEntries, maxBytes: maxBytes,
	}
}

// ProcessCacheStats returns a consistent snapshot of the shared analysis cache.
func ProcessCacheStats() CacheStats { return processAnalysisCache.stats() }

func (c *analysisCache) analyze(ctx context.Context, filenameHint string, src []byte, build func() Result) Result {
	key := analysisCacheKey(filenameHint, src)
	var flight *analysisFlight
	for {
		if ctx.Err() != nil {
			return canceledAnalysis(ctx, filenameHint, src)
		}
		c.mu.Lock()
		if element := c.entries[key]; element != nil {
			cached := element.Value.(*analysisCacheEntry)
			if !cached.expires.IsZero() && time.Now().After(cached.expires) {
				delete(c.entries, key)
				c.order.Remove(element)
				c.bytes -= cached.bytes
			} else {
				c.hits++
				c.order.MoveToFront(element)
				result := cloneResult(cached.result)
				c.mu.Unlock()
				return result
			}
		}
		if active := c.flights[key]; active != nil {
			c.joins++
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return canceledAnalysis(ctx, filenameHint, src)
			case <-active.done:
				if analysisWasCanceled(active.result) {
					continue
				}
				return cloneResult(active.result)
			}
		}
		flight = &analysisFlight{done: make(chan struct{})}
		c.flights[key] = flight
		c.misses++
		c.mu.Unlock()
		break
	}
	result := build()
	if ctx.Err() != nil {
		result = canceledAnalysis(ctx, filenameHint, src)
	}
	retained := cloneResult(result)
	weight := analysisResultBytes(key, retained)

	c.mu.Lock()
	flight.result = retained
	// Request cancellation is excluded from the source cache.
	if !analysisWasCanceled(retained) && weight <= c.maxBytes/4 {
		c.storeLocked(key, retained, weight)
	} else {
		c.bypasses++
	}
	delete(c.flights, key)
	close(flight.done)
	c.mu.Unlock()
	return cloneResult(retained)
}

func analysisWasCanceled(result Result) bool {
	return result.ParseFailure != nil && (result.ParseFailure.Reason == "canceled" || result.ParseFailure.Reason == "deadline")
}

func canceledAnalysis(ctx context.Context, filename string, src []byte) Result {
	return Result{Path: filename, SizeBytes: int64(len(src)), ParseFailure: tsparse.CancellationFailure(ctx, "", len(src))}
}

func (c *analysisCache) storeLocked(key string, result Result, weight int64) {
	entry := &analysisCacheEntry{key: key, result: result, bytes: weight}
	// Failed snapshots back off before another parse.
	if result.DefinitionError() != nil {
		entry.expires = time.Now().Add(30 * time.Second)
	}
	c.entries[key] = c.order.PushFront(entry)
	c.bytes += weight
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		old := oldest.Value.(*analysisCacheEntry)
		delete(c.entries, old.key)
		c.order.Remove(oldest)
		c.bytes -= old.bytes
		c.evictions++
	}
}

func (c *analysisCache) stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{
		Entries: c.order.Len(), Bytes: c.bytes, Hits: c.hits, Misses: c.misses,
		Joins: c.joins, Evictions: c.evictions, Bypasses: c.bypasses,
	}
}

func analysisCacheKey(filenameHint string, src []byte) string {
	name := filepath.Base(strings.TrimSpace(filenameHint))
	digest := sha256.Sum256(src)
	cfg, err := tsparse.LoadConfig()
	return fmt.Sprintf("%s\x00%s\x00%d:%v\x00%s", analysisCacheKeyVersion, name, cfg.AnalysisTimeoutMS, err, hex.EncodeToString(digest[:]))
}

func analysisResultBytes(key string, result Result) int64 {
	size := int64(len(key) + len(result.Path) + len(result.Source) + len(result.Language))
	for _, symbol := range result.Symbols {
		size += int64(32 + len(symbol.Kind) + len(symbol.Name))
	}
	for _, definition := range result.Definitions {
		size += int64(64 + len(definition.Kind) + len(definition.Name))
	}
	for _, diagnostic := range result.Errors {
		size += int64(32 + len(diagnostic.Snippet) + len(diagnostic.Kind))
	}
	if result.ParseFailure != nil {
		size += int64(96 + len(result.ParseFailure.Error()))
	}
	if result.LogDigest != nil {
		size += logDigestBytes(result.LogDigest)
	}
	if result.Diagnostics != nil {
		size += int64(64 + len(result.Diagnostics.Hint) + len(result.Diagnostics.HintCode))
	}
	return max(size, 1)
}

func logDigestBytes(digest *logoutline.Digest) int64 {
	if digest == nil {
		return 0
	}
	size := int64(96)
	if digest.TimeSpan != nil {
		size += int64(len(digest.TimeSpan.Start) + len(digest.TimeSpan.End))
	}
	for _, field := range digest.Fields {
		size += int64(24 + len(field.Key))
	}
	for _, facet := range digest.Facets {
		size += int64(24 + len(facet.Key))
		for _, value := range facet.Values {
			size += int64(24 + len(value.Value))
		}
	}
	for _, cluster := range digest.Clusters {
		size += int64(40 + len(cluster.Template) + len(cluster.Severity))
	}
	return size
}

func cloneResult(result Result) Result {
	cloned := result
	if result.ParseFailure != nil {
		failure := *result.ParseFailure
		cloned.ParseFailure = &failure
	}
	cloned.Symbols = append([]Symbol(nil), result.Symbols...)
	cloned.Definitions = append([]repomap.DefinitionSpan(nil), result.Definitions...)
	cloned.Errors = append([]SyntaxDiagnostic(nil), result.Errors...)
	if result.Parses != nil {
		parses := *result.Parses
		cloned.Parses = &parses
	}
	if result.Diagnostics != nil {
		diagnostics := *result.Diagnostics
		cloned.Diagnostics = &diagnostics
	}
	cloned.LogDigest = cloneLogDigest(result.LogDigest)
	return cloned
}

func cloneLogDigest(digest *logoutline.Digest) *logoutline.Digest {
	if digest == nil {
		return nil
	}
	cloned := *digest
	if digest.TimeSpan != nil {
		span := *digest.TimeSpan
		cloned.TimeSpan = &span
	}
	cloned.Fields = append([]logoutline.FieldStat(nil), digest.Fields...)
	cloned.Facets = append([]logoutline.Facet(nil), digest.Facets...)
	for i := range cloned.Facets {
		cloned.Facets[i].Values = append([]logoutline.FacetValue(nil), digest.Facets[i].Values...)
	}
	cloned.Clusters = append([]logoutline.Cluster(nil), digest.Clusters...)
	return &cloned
}
