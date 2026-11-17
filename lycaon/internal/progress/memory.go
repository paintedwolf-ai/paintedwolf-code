package progress

import (
	"context"
	"sync"
)

// MemoryStore is an in-memory Store for tests and fallback.
type MemoryStore struct {
	mu     sync.Mutex
	bySess map[string]runRecord
}

type runRecord struct {
	runID    string
	content  string
	itemIDs  []string
	revision uint64
}

var _ RunScopedStore = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory progress store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{bySess: make(map[string]runRecord)}
}

func (s *MemoryStore) Set(sessionID, content string) error {
	_, err := s.Update(context.Background(), sessionID, func(Snapshot) (Document, error) {
		return Document{Content: content, ItemIDs: mintItemIDs(clampContent(content))}, nil
	})
	return err
}

func (s *MemoryStore) Get(_ context.Context, sessionID string) string {
	if s == nil {
		return ""
	}
	key := normalizeKey(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bySess[key].content
}

func (s *MemoryStore) BoundRunID(sessionID string) string {
	if s == nil {
		return ""
	}
	key := normalizeKey(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bySess[key].runID
}
