package progressclosure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/progress"
)

func reconciliation(root string, baseline guard.ProgressClosureBaseline) *progress.Reconciliation {
	jobs := slices.Clone(baseline.JobIDs)
	slices.Sort(jobs)
	raw, _ := json.Marshal(struct {
		Root, Content string
		Jobs          []string
	}{root, baseline.Content, jobs})
	digest := sha256.Sum256(raw)
	return &progress.Reconciliation{Token: hex.EncodeToString(digest[:]), Jobs: jobs}
}

// Reconciliation exposes the exact worker finishes awaiting acknowledgement.
func (m *Service) Reconciliation(root string) *progress.Reconciliation {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	baseline, armed := m.baselines.Load(root)
	if !armed {
		return nil
	}
	return reconciliation(root, baseline)
}

// Acknowledge clears only the worker-finish set identified by token.
func (m *Service) Acknowledge(root, token string) bool {
	if m == nil || token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	baseline, armed := m.baselines.Load(root)
	if !armed {
		return false
	}
	if reconciliation(root, baseline).Token != token {
		return false
	}
	m.baselines.Delete(root)
	return true
}
