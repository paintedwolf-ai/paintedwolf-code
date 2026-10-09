package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
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
	tasks, err := c.m.reviewEvidenceTasks(ctx, parentSessionID, since)
	if err != nil {
		return nil, err
	}
	legs := make([]guidance.EvidenceLeg, 0, len(tasks))
	for _, task := range tasks {
		if child := strings.TrimSpace(task.ChildSessionID); child != "" {
			legs = append(legs, guidance.EvidenceLeg{ChildSessionID: child, LegID: strings.TrimSpace(task.LegID)})
		}
	}
	return legs, nil
}

// reviewEvidenceTasks uses workflow ownership when present and intent time otherwise.
func (m *Manager) reviewEvidenceTasks(ctx context.Context, sessionID string, since time.Time) ([]api.WorkerTask, error) {
	if m == nil || m.workerQueue == nil || m.store == nil {
		return nil, nil
	}
	parent, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	runID := ""
	if m.workflows != nil {
		run, err := m.workflows.Runs.ActiveBySession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if run != nil && !runstate.IsAmbientRun(run) {
			runID = run.ID
		}
	}
	tasks, err := m.workerQueue.ListBySession(ctx, parent.ProjectID, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]api.WorkerTask, 0, len(tasks))
	for _, task := range tasks {
		if runID != "" {
			if task.WorkflowRunID != runID {
				continue
			}
		} else if !since.IsZero() && task.CreatedAt.Before(since) {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}
