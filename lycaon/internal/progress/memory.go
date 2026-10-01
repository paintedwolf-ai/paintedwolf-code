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
	runID   string
	content string
}

var _ RunScopedStore = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory progress store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{bySess: make(map[string]runRecord)}
}

func (s *MemoryStore) Set(sessionID, content string) error {
	if s == nil {
		return nil
	}
	key := normalizeKey(sessionID)
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.bySess[key]
	rec.content = clampContent(content)
	s.bySess[key] = rec
	return nil
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

func (s *MemoryStore) BindRun(sessionID, workflowRunID string) {
	if s == nil {
		return
	}
	key := normalizeKey(sessionID)
	workflowRunID = normalizeKey(workflowRunID)
	if key == "" || workflowRunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.bySess[key]
	rec.runID = workflowRunID
	s.bySess[key] = rec
}

func (s *MemoryStore) EnsureRun(sessionID, workflowRunID, goal string) (refreshed bool) {
	if s == nil {
		return false
	}
	key := normalizeKey(sessionID)
	workflowRunID = normalizeKey(workflowRunID)
	if key == "" || workflowRunID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bySess[key] = runRecord{
		runID:   workflowRunID,
		content: clampContent(formatBootstrapContent(goal)),
	}
	return true
}
