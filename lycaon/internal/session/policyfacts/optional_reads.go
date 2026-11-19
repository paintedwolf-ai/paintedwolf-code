package policyfacts

import (
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
)

const (
	maxOptionalReadNotices = 512
	optionalReadNoticeCode = "OPTIONAL_PROTECTED_READ_WITHHELD"
)

type optionalReadNotices struct {
	mu    sync.Mutex
	shown map[string]map[string]struct{}
}

// ForgetSession releases a chat's optional read observations.
func (m *Service) ForgetSession(sessionID string) {
	m.optionalReads.mu.Lock()
	defer m.optionalReads.mu.Unlock()
	delete(m.optionalReads.shown, sessionID)
}

func (n *optionalReadNotices) observe(sessionID string, facts guidance.ToolResultFacts) []string {
	observation := facts.Confine
	if sessionID == "" || !facts.Succeeded() || !facts.CommandExitedZero || observation.Running ||
		len(observation.FailedStages) != 0 || observation.Destination != "" || len(observation.Refusals.Refusals) == 0 {
		return nil
	}
	for _, refusal := range observation.Refusals.Refusals {
		if refusal.Recovery != confine.RecoverReadPath || refusal.Target == "" {
			return nil
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.shown == nil {
		n.shown = make(map[string]map[string]struct{})
	}
	seen := n.shown[sessionID]
	if seen == nil {
		seen = make(map[string]struct{})
		n.shown[sessionID] = seen
	}
	var paths []string
	for _, refusal := range observation.Refusals.Refusals {
		if len(seen) >= maxOptionalReadNotices {
			break
		}
		if _, exists := seen[refusal.Target]; !exists {
			seen[refusal.Target] = struct{}{}
			paths = append(paths, refusal.Target)
		}
	}
	return paths
}

func (n *optionalReadNotices) release(sessionID string, paths []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	seen := n.shown[sessionID]
	for _, path := range paths {
		delete(seen, path)
	}
	if len(seen) == 0 {
		delete(n.shown, sessionID)
	}
}
