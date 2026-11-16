package decisions

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// Memory stores decisions for tests.
type Memory struct {
	mu      sync.Mutex
	byChild map[string]api.WorkerDecisionRequest
}

// NewMemory returns an empty decision store.
func NewMemory() *Memory {
	return &Memory{byChild: make(map[string]api.WorkerDecisionRequest)}
}

func (s *Memory) Put(_ context.Context, decision api.WorkerDecisionRequest) error {
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

func (s *Memory) Get(_ context.Context, childSessionID string) (api.WorkerDecisionRequest, bool, error) {
	if s == nil {
		return api.WorkerDecisionRequest{}, false, nil
	}
	childSessionID = strings.TrimSpace(childSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	decision, ok := s.byChild[childSessionID]
	return cloneDecision(decision), ok, nil
}
