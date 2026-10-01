package session

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
)

// CloseoutEvidence reads what a session's closeout may cite: its own ledger
// and the ledgers of the worker legs its job records name.
func (m *Manager) CloseoutEvidence() guidance.CloseoutEvidenceReader {
	return closeoutEvidence{m: m}
}

type closeoutEvidence struct{ m *Manager }

func (c closeoutEvidence) LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error) {
	return c.m.store.LoadLedger(ctx, sessionID)
}

func (c closeoutEvidence) WorkerLegs(ctx context.Context, parentSessionID string, since time.Time) ([]guidance.EvidenceLeg, error) {
	if c.m == nil || c.m.workerQueue == nil || c.m.store == nil {
		return nil, nil
	}
	parent, err := c.m.store.Get(ctx, parentSessionID)
	if err != nil {
		return nil, err
	}
	tasks, err := c.m.workerQueue.ListBySession(ctx, parent.ProjectID, parentSessionID)
	if err != nil {
		return nil, err
	}
	legs := make([]guidance.EvidenceLeg, 0, len(tasks))
	for _, task := range tasks {
		child := strings.TrimSpace(task.ChildSessionID)
		if child == "" || (!since.IsZero() && task.CreatedAt.Before(since)) {
			continue
		}
		legs = append(legs, guidance.EvidenceLeg{ChildSessionID: child, LegID: strings.TrimSpace(task.LegID)})
	}
	return legs, nil
}
