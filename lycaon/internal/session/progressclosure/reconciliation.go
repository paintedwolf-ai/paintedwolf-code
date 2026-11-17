package progressclosure

import (
	"context"
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
	raw, _ := json.Marshal(struct { //nolint:errchkjson // Strings always encode.
		Root, Revision string
		Jobs           []string
	}{root, baseline.Revision, jobs})
	digest := sha256.Sum256(raw)
	return &progress.Reconciliation{Token: hex.EncodeToString(digest[:]), Jobs: jobs, Settled: baseline.SettledWork()}
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

// Acknowledge clears only the worker-finish set identified by token, after
// recording each listed result as accounted for. A failed record leaves the
// set armed, so the checklist update can acknowledge it again.
func (m *Service) Acknowledge(ctx context.Context, root, token string) bool {
	if m == nil || token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	baseline, armed := m.baselines.Load(root)
	if !armed {
		return false
	}
	rec := reconciliation(root, baseline)
	if rec.Token != token {
		return false
	}
	if m.accounts != nil {
		if err := m.accounts.AccountJobs(ctx, rec.Jobs); err != nil {
			return false
		}
	}
	m.baselines.Delete(root)
	return true
}
