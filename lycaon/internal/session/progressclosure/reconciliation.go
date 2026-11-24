package progressclosure

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

// Reconciliation exposes the settled worker finishes awaiting acknowledgement.
func (m *Service) Reconciliation(root string) *progress.Reconciliation {
	if m == nil {
		return nil
	}
	root = strings.TrimSpace(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	baseline, armed := m.baselines.Load(root)
	if !armed {
		return nil
	}
	jobs := slices.Clone(baseline.JobIDs)
	slices.Sort(jobs)
	unaccounted := slices.Clone(baseline.Unaccounted)
	if unaccounted == nil {
		unaccounted = []progress.UnaccountedResult{}
	}
	return &progress.Reconciliation{
		Jobs:        jobs,
		Settled:     baseline.SettledWork(),
		Unaccounted: unaccounted,
	}
}

// UnaccountedResults returns the model-facing result handles awaiting acknowledgement.
func (m *Service) UnaccountedResults(root string) []progress.UnaccountedResult {
	rec := m.Reconciliation(root)
	if rec == nil || len(rec.Unaccounted) == 0 {
		return []progress.UnaccountedResult{}
	}
	return slices.Clone(rec.Unaccounted)
}

// AcknowledgeTx resolves result handles in coordinator history and records durable
// accounting inside tx. In-memory latches remain armed until Release runs post-commit.
func (m *Service) AcknowledgeTx(ctx context.Context, tx *sql.Tx, root, principalID string, handles []string) (progress.AckOutcome, error) {
	if m == nil || len(handles) == 0 {
		return progress.AckOutcome{}, nil
	}
	root = strings.TrimSpace(root)
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		principalID = root
	}
	m.mu.Lock()
	accounts := m.accounts
	baseline, armed := m.baselines.Load(root)
	m.mu.Unlock()

	if accounts != nil {
		actor := workerrecords.Actor{
			SessionID:   root,
			Role:        workerrecords.RoleCoordinator,
			PrincipalID: principalID,
		}
		var jobIDs []string
		var unknown []string
		for _, h := range handles {
			h = strings.TrimSpace(h)
			ref, err := accounts.ResolveResultForAckTx(ctx, tx, actor, h)
			if errors.Is(err, workerrecords.ErrResultNotFound) || errors.Is(err, workerrecords.ErrCoordinatorOnly) {
				unknown = append(unknown, h)
				continue
			}
			if err != nil {
				return progress.AckOutcome{}, err
			}
			if !slices.Contains(jobIDs, ref.JobID) {
				jobIDs = append(jobIDs, ref.JobID)
			}
		}
		if len(unknown) > 0 {
			return progress.AckOutcome{UnknownHandles: unknown}, nil
		}
		if err := accounts.AccountJobsTx(ctx, tx, jobIDs); err != nil {
			return progress.AckOutcome{}, err
		}
		return progress.AckOutcome{AccountedJobIDs: jobIDs}, nil
	}

	if !armed {
		return progress.AckOutcome{UnknownHandles: slices.Clone(handles)}, nil
	}
	var jobIDs []string
	var unknown []string
	for _, h := range handles {
		h = strings.TrimSpace(h)
		matched := false
		for i, u := range baseline.Unaccounted {
			if u.Handle == h && i < len(baseline.JobIDs) {
				matched = true
				if !slices.Contains(jobIDs, baseline.JobIDs[i]) {
					jobIDs = append(jobIDs, baseline.JobIDs[i])
				}
				break
			}
		}
		if !matched {
			unknown = append(unknown, h)
		}
	}
	if len(unknown) > 0 {
		return progress.AckOutcome{UnknownHandles: unknown}, nil
	}
	return progress.AckOutcome{AccountedJobIDs: jobIDs}, nil
}
