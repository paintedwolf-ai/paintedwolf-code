package editordoc

import (
	"context"
	"errors"
	"sync"
)

var ErrAgentReadRequired = errors.New("the agent must read the shared document before editing it")

const maxAgentReadBases = 4096

// ReadBasisLookup answers the revision a response may edit a document from.
// A response freezes its bases before its tools run; nil means no freeze.
type ReadBasisLookup func(documentID string) (revision int64, ok bool)

type agentReadKey struct{ project, session, document string }
type agentReadBasis struct {
	revision int64
	used     uint64
}
type agentReadCache struct {
	mu    sync.Mutex
	clock uint64
	bases map[agentReadKey]agentReadBasis
}

// Read leases expire on restart or eviction; editing then requires a fresh read.
func (s *Service) RememberAgentRead(projectID, sessionID, documentID string, revision int64) {
	if sessionID == "" || revision <= 0 {
		return
	}
	c := &s.agentReads
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bases == nil {
		c.bases = make(map[agentReadKey]agentReadBasis)
	}
	key := agentReadKey{projectID, sessionID, documentID}
	if previous, ok := c.bases[key]; ok && previous.revision > revision {
		return
	}
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
	c.clock++
	c.bases[key] = agentReadBasis{revision, c.clock}
}

// DropAgentRead withdraws a chat's basis for a document; the next write needs a read.
func (s *Service) DropAgentRead(projectID, sessionID, documentID string) {
	s.agentReads.mu.Lock()
	defer s.agentReads.mu.Unlock()
	delete(s.agentReads.bases, agentReadKey{projectID, sessionID, documentID})
}

func (s *Service) AgentReadBase(ctx context.Context, projectID, sessionID, documentID string, frozen ReadBasisLookup) (*Document, error) {
	c := &s.agentReads
	c.mu.Lock()
	key := agentReadKey{projectID, sessionID, documentID}
	basis, ok := c.bases[key]
	if frozen != nil {
		basis.revision, ok = frozen(documentID)
	}
	if ok && frozen == nil {
		c.clock++
		basis.used = c.clock
		c.bases[key] = basis
	}
	c.mu.Unlock()
	if !ok {
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
	return d, err
}

// FreezeAgentReads binds a model response to reads completed before its tools
// execute; a read in that response cannot advance its own bases.
func (s *Service) FreezeAgentReads(projectID, sessionID string) map[string]int64 {
	c := &s.agentReads
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64)
	for key, basis := range c.bases {
		if key.project == projectID && key.session == sessionID {
			out[key.document] = basis.revision
		}
	}
	return out
}
