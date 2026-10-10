package promotionstate

import (
	"sync"

	"github.com/lycaon/lycaon/internal/scopedstore"
)

type Service struct {
	mu                sync.Mutex
	mergeReconcile    scopedstore.LRU[map[string]struct{}]
	promotePathStatus scopedstore.LRU[*promotePathSessionStore]
}

func New() *Service { return &Service{} }
func (s *Service) Forget(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mergeReconcile.Delete(sessionID)
	s.promotePathStatus.Delete(sessionID)
}
