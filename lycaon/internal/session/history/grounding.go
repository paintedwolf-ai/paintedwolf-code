package history

import (
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/llm/compaction"
)

// WorkerGroundingRetries returns in-session evidence-grounding retry budget for workers.
func (m *Service) WorkerGroundingRetries() int {
	maxRetries := compaction.DefaultCompactionConfig().MaxWorkerGroundingRetries
	if m != nil && m.Compactor != nil {
		if n := m.Compactor.Config().MaxWorkerGroundingRetries; n > 0 {
			maxRetries = n
		}
	}
	if maxRetries <= 0 {
		maxRetries = limits.DefaultWorkerGroundingRetries
	}
	return maxRetries
}
