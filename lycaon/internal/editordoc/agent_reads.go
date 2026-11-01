package editordoc

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/lycaon/lycaon/internal/documentcore"
)

type AgentReadDocument struct {
	*Document
	ReadSpans []documentcore.LineSpan
	ReadWhole bool
}

var ErrAgentReadRequired = errors.New("the agent must read the shared document before editing it")

const maxAgentReadBases = 4096
const maxAgentReadSpans = 256

// ReadBasisLookup answers the revision a response may edit a document from.
// A response freezes its bases before its tools run; nil means no freeze.
type ReadBasisLookup func(documentID string) (basis documentcore.ReadBasis, ok bool)

type agentReadKey struct{ project, session, document string }
type agentReadBasis struct {
	revision int64
	whole    bool
	spans    []documentcore.LineSpan
	used     uint64
}
type agentReadCache struct {
	mu    sync.Mutex
	clock uint64
	bases map[agentReadKey]agentReadBasis
}

// Read leases expire on restart or eviction; editing then requires a fresh read.
func (a *AgentService) RememberAgentRead(projectID, sessionID, documentID string, revision int64) {
	if sessionID == "" || revision <= 0 {
		return
	}
	c := &a.agentReads
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureMapLocked()
	key := agentReadKey{projectID, sessionID, documentID}
	if previous, ok := c.bases[key]; ok && previous.revision > revision {
		return
	}
	c.evictIfNeededLocked(key)
	c.clock++
	c.bases[key] = agentReadBasis{revision: revision, whole: true, used: c.clock}
}

// RememberAgentReadSpans records that a session saw specific 1-based inclusive
// line ranges of a document at revision. Multiple span reads at the same
// revision merge; a whole-document read at that revision stays whole.
func (a *AgentService) RememberAgentReadSpans(projectID, sessionID, documentID string, revision int64, spans []documentcore.LineSpan) {
	if sessionID == "" || revision <= 0 {
		return
	}
	normalized := normalizeLineSpans(spans)
	if len(normalized) == 0 || len(normalized) > maxAgentReadSpans {
		return
	}
	c := &a.agentReads
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureMapLocked()
	key := agentReadKey{projectID, sessionID, documentID}
	if previous, ok := c.bases[key]; ok {
		if previous.revision > revision {
			return
		}
		if previous.revision == revision {
			c.clock++
			if previous.whole {
				previous.used = c.clock
				c.bases[key] = previous
				return
			}
			merged := normalizeLineSpans(append(append([]documentcore.LineSpan(nil), previous.spans...), normalized...))
			if len(merged) > maxAgentReadSpans {
				delete(c.bases, key)
				return
			}
			c.bases[key] = agentReadBasis{revision: revision, spans: merged, used: c.clock}
			return
		}
	}
	c.evictIfNeededLocked(key)
	c.clock++
	c.bases[key] = agentReadBasis{revision: revision, spans: normalized, used: c.clock}
}

func (c *agentReadCache) ensureMapLocked() {
	if c.bases == nil {
		c.bases = make(map[agentReadKey]agentReadBasis)
	}
}

func (c *agentReadCache) evictIfNeededLocked(key agentReadKey) {
	if _, exists := c.bases[key]; !exists && len(c.bases) >= maxAgentReadBases {
		var oldest agentReadKey
		used := ^uint64(0)
		for candidate, basis := range c.bases {
			if basis.used < used {
				oldest, used = candidate, basis.used
			}
		}
		delete(c.bases, oldest)
	}
}

func (c *agentReadCache) replaceRevision(projectID, documentID string, fromRevision, toRevision int64) {
	if fromRevision <= 0 || toRevision <= 0 || fromRevision == toRevision {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, basis := range c.bases {
		if key.project == projectID && key.document == documentID && basis.revision == fromRevision {
			basis.revision = toRevision
			c.bases[key] = basis
		}
	}
}

func normalizeLineSpans(spans []documentcore.LineSpan) []documentcore.LineSpan {
	valid := make([]documentcore.LineSpan, 0, len(spans))
	for _, s := range spans {
		if s.StartLine >= 1 && s.EndLine >= s.StartLine {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return nil
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].StartLine != valid[j].StartLine {
			return valid[i].StartLine < valid[j].StartLine
		}
		return valid[i].EndLine < valid[j].EndLine
	})
	merged := valid[:1]
	for _, s := range valid[1:] {
		last := &merged[len(merged)-1]
		if s.StartLine <= last.EndLine+1 {
			if s.EndLine > last.EndLine {
				last.EndLine = s.EndLine
			}
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// DropAgentRead withdraws a chat's basis for a document; the next write needs a read.
func (a *AgentService) DropAgentRead(projectID, sessionID, documentID string) {
	a.agentReads.mu.Lock()
	defer a.agentReads.mu.Unlock()
	delete(a.agentReads.bases, agentReadKey{projectID, sessionID, documentID})
}

func (a *AgentService) AgentReadBase(ctx context.Context, projectID, sessionID, documentID string, frozen ReadBasisLookup) (*AgentReadDocument, error) {
	s := a.s
	c := &s.agentReads
	c.mu.Lock()
	key := agentReadKey{projectID, sessionID, documentID}
	basis, ok := c.bases[key]
	if frozen != nil {
		frozenBasis, frozenOK := frozen(documentID)
		if !frozenOK {
			ok = false
		} else {
			basis = agentReadBasis{revision: frozenBasis.Revision, whole: frozenBasis.Whole, spans: append([]documentcore.LineSpan(nil), frozenBasis.Spans...)}
			ok = true
		}
	}
	if ok && frozen == nil {
		c.clock++
		basis.used = c.clock
		c.bases[key] = basis
	}
	c.mu.Unlock()
	if !ok || (!basis.whole && len(basis.spans) == 0) {
		return nil, ErrAgentReadRequired
	}
	d, err := s.Pin(ctx, projectID, documentID, basis.revision)
	if errors.Is(err, ErrRevisionConflict) || errors.Is(err, ErrNotFound) {
		s.agentReads.mu.Lock()
		if s.agentReads.bases[key].revision == basis.revision {
			delete(s.agentReads.bases, key)
		}
		s.agentReads.mu.Unlock()
		return nil, ErrAgentReadRequired
	}
	if d == nil {
		return nil, err
	}
	out := &AgentReadDocument{Document: d, ReadWhole: basis.whole}
	if !basis.whole {
		out.ReadSpans = append([]documentcore.LineSpan(nil), basis.spans...)
	}
	return out, err
}

// FreezeAgentReads binds a model response to reads completed before its tools
// execute; a read in that response cannot advance its own bases.
func (a *AgentService) FreezeAgentReads(projectID, sessionID string) map[string]documentcore.ReadBasis {
	c := &a.agentReads
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]documentcore.ReadBasis)
	for key, basis := range c.bases {
		if key.project == projectID && key.session == sessionID {
			out[key.document] = documentcore.ReadBasis{Revision: basis.revision, Whole: basis.whole, Spans: append([]documentcore.LineSpan(nil), basis.spans...)}
		}
	}
	return out
}
