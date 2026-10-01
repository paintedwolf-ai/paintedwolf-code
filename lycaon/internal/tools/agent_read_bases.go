package tools

import "sync"

// AgentReadBases is the set of document revisions one model response may
// edit from: frozen from reads completed before its tools run, advanced by
// its own writes whose result is exactly the proposed text.
type AgentReadBases struct {
	mu        sync.Mutex
	revisions map[string]int64
}

// NewAgentReadBases freezes the given document revisions.
func NewAgentReadBases(revisions map[string]int64) *AgentReadBases {
	frozen := make(map[string]int64, len(revisions))
	for id, revision := range revisions {
		frozen[id] = revision
	}
	return &AgentReadBases{revisions: frozen}
}

// Lookup answers the revision the response may edit the document from.
func (b *AgentReadBases) Lookup(documentID string) (int64, bool) {
	if b == nil {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	revision, ok := b.revisions[documentID]
	return revision, ok
}

// Advance records a basis the response established itself.
func (b *AgentReadBases) Advance(documentID string, revision int64) {
	if b == nil || revision <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revisions[documentID] = revision
}

// Forget withdraws the response's basis for a document.
func (b *AgentReadBases) Forget(documentID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.revisions, documentID)
}
