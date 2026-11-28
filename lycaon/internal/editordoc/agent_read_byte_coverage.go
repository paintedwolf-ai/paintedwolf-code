package editordoc

import (
	"sort"

	"github.com/lycaon/lycaon/internal/documentcore"
)

const maxAgentReadByteRanges = 64

type agentReadByteRange struct{ start, end int64 }

type agentReadByteCoverage struct {
	revision int64
	source   documentcore.ReadSource
	ranges   []agentReadByteRange
}

// RememberAgentReadBytes accumulates verified, delivered ranges of one source.
// Partial ranges do not grant a basis. Complete coverage becomes a whole read
// of the same document revision through the ordinary frozen-basis path.
func (a *AgentService) RememberAgentReadBytes(projectID, sessionID, documentID string, revision int64, source documentcore.ReadSource, start, end int64) {
	if sessionID == "" || revision <= 0 || source.ID == "" || source.SHA256 == "" || source.Representation == "" ||
		start < 0 || end <= start || end > source.Bytes {
		return
	}
	c := &a.agentReads
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureMapLocked()
	key := agentReadKey{projectID, sessionID, documentID}
	basis := c.bases[key]
	if revision < basis.revision || revision < basis.pending.revision || (revision == basis.revision && basis.whole) {
		return
	}
	c.evictIfNeededLocked(key)
	c.clock++
	basis.used = c.clock
	if basis.pending.revision != revision || basis.pending.source != source {
		basis.pending = agentReadByteCoverage{revision: revision, source: source}
	}
	ranges := mergeAgentReadByteRange(basis.pending.ranges, agentReadByteRange{start: start, end: end})
	switch {
	case len(ranges) > maxAgentReadByteRanges:
		// Keep the revision and source identity even after dropping coverage.
		basis.pending.ranges = nil
	case len(ranges) == 1 && ranges[0].start == 0 && ranges[0].end == source.Bytes:
		basis = agentReadBasis{revision: revision, whole: true, used: c.clock}
	default:
		basis.pending.ranges = ranges
	}
	c.bases[key] = basis
}

func mergeAgentReadByteRange(ranges []agentReadByteRange, added agentReadByteRange) []agentReadByteRange {
	ranges = append(ranges, added)
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	merged := ranges[:1]
	for _, current := range ranges[1:] {
		last := &merged[len(merged)-1]
		if current.start <= last.end {
			last.end = max(last.end, current.end)
		} else {
			merged = append(merged, current)
		}
	}
	return merged
}
