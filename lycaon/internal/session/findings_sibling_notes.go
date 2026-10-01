package session

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/workercontext"
)

// RecentSiblingNotes returns fresh findings from sibling workers.
func (m *Manager) RecentSiblingNotes(ctx context.Context, childSessionID string, afterID int64, max int) ([]inject.SiblingNote, int64, error) {
	if m == nil || m.findings == nil || strings.TrimSpace(childSessionID) == "" {
		return nil, afterID, nil
	}
	root := RootSessionID(ctx, m.store, childSessionID)
	if strings.TrimSpace(root) == "" {
		return nil, afterID, nil
	}
	ownJob := ""
	batchStart := time.Time{}
	if m.workerQueue != nil {
		if task, ok := m.workerQueue.Get(workercontext.Job(ctx)); ok && task != nil {
			ownJob = strings.TrimSpace(task.ID)
			var err error
			batchStart, err = m.findingsBoundary(ctx, root, task.CreatedAt)
			if err != nil {
				return nil, afterID, err
			}
		}
	}
	found, nextID, err := m.findings.Recent(ctx, root, ownJob, afterID, max, batchStart)
	if err != nil {
		return nil, afterID, err
	}
	notes := make([]inject.SiblingNote, 0, len(found)+max)
	for _, f := range found {
		notes = append(notes, inject.SiblingNote{ID: f.ID, HasBody: f.Body != "", Agent: f.Agent, Summary: f.Summary, Ref: f.Ref})
	}
	return notes, nextID, nil
}

// ListFindings returns recent worker findings for the root session (worklog view, newest last).
func (m *Manager) ListFindings(ctx context.Context, rootSessionID string, max int) ([]findings.Finding, error) {
	if m == nil || m.findings == nil {
		return nil, nil
	}
	return m.findings.List(ctx, rootSessionID, max)
}

// SetFindingsStore wires the shared record_finding findings store for sibling-note injection.
func (m *Manager) SetFindingsStore(store findings.Store) {
	if m == nil {
		return
	}
	m.findings = store
}

// SetPeerRejectionFeed wires cross-worker tool-reject notes for sibling inject.
func (m *Manager) SetPeerRejectionFeed(feed *PeerRejectionFeed) {
	if m == nil {
		return
	}
	m.peerRejections = feed
}

// SetProgressStore wires the coordinator progress store for the plan-missing nudge.
func (m *Manager) SetProgressStore(store progress.RunScopedStore) {
	if m == nil {
		return
	}
	m.progress = store
}
