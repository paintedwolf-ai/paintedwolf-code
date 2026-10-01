package session

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// MemoryDecisionStore stores decisions for tests.
type MemoryDecisionStore struct {
	mu      sync.Mutex
	byChild map[string]api.WorkerDecisionRequest
}

// NewMemoryDecisionStore returns an empty decision store.
func NewMemoryDecisionStore() *MemoryDecisionStore {
	return &MemoryDecisionStore{byChild: make(map[string]api.WorkerDecisionRequest)}
}

func (s *MemoryDecisionStore) Put(_ context.Context, decision api.WorkerDecisionRequest) error {
	decision, err := normalizeDecision(decision)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for child, current := range s.byChild {
		if child != decision.ChildSessionID && current.WorkerID == decision.WorkerID {
			return fmt.Errorf("decision job %q already belongs to child %q", decision.WorkerID, child)
		}
	}
	s.byChild[decision.ChildSessionID] = cloneDecision(decision)
	return nil
}

func (s *MemoryDecisionStore) Get(_ context.Context, childSessionID string) (api.WorkerDecisionRequest, bool, error) {
	if s == nil {
		return api.WorkerDecisionRequest{}, false, nil
	}
	childSessionID = strings.TrimSpace(childSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	decision, ok := s.byChild[childSessionID]
	return cloneDecision(decision), ok, nil
}

func (s *MemoryDecisionStore) GetByJob(_ context.Context, jobID string) (api.WorkerDecisionRequest, bool, error) {
	if s == nil {
		return api.WorkerDecisionRequest{}, false, nil
	}
	jobID = strings.TrimSpace(jobID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, decision := range s.byChild {
		if decision.WorkerID == jobID {
			return cloneDecision(decision), true, nil
		}
	}
	return api.WorkerDecisionRequest{}, false, nil
}

func cloneDecision(decision api.WorkerDecisionRequest) api.WorkerDecisionRequest {
	decision.Options = append([]string(nil), decision.Options...)
	decision.ArtifactIDs = append([]string(nil), decision.ArtifactIDs...)
	return decision
}

func (s *MemoryDecisionStore) Clear(_ context.Context, childSessionID string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byChild, strings.TrimSpace(childSessionID))
	return nil
}
