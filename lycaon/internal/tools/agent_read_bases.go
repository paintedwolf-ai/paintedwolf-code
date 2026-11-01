package tools

import (
	"sync"

	"github.com/lycaon/lycaon/internal/documentcore"
)

// AgentReadBases is the set of document revisions one model response may
// edit from: frozen from reads completed before its tools run, advanced by
// its own writes whose result is exactly the proposed text.
type AgentReadBases struct {
	mu        sync.Mutex
	revisions map[string]documentcore.ReadBasis
}

// NewAgentReadBases freezes the given document revisions.
func NewAgentReadBases(revisions map[string]documentcore.ReadBasis) *AgentReadBases {
	frozen := make(map[string]documentcore.ReadBasis, len(revisions))
	for id, revision := range revisions {
		revision.Spans = append([]documentcore.LineSpan(nil), revision.Spans...)
		frozen[id] = revision
	}
	return &AgentReadBases{revisions: frozen}
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

func (b *AgentReadBases) Basis(documentID string) (documentcore.ReadBasis, bool) {
	if b == nil {
		return documentcore.ReadBasis{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	basis, ok := b.revisions[documentID]
	basis.Spans = append([]documentcore.LineSpan(nil), basis.Spans...)
	return basis, ok
}
func (b *AgentReadBases) AdvanceBasis(documentID string, basis documentcore.ReadBasis) {
	if b == nil || basis.Revision <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !basis.Whole && len(basis.Spans) == 0 {
		delete(b.revisions, documentID)
		return
	}
	basis.Spans = append([]documentcore.LineSpan(nil), basis.Spans...)
	b.revisions[documentID] = basis
}
