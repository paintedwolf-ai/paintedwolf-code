package session

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/session/workercontext"
)

// findingsBoundary includes agreements published before a sibling started.
func (m *Manager) findingsBoundary(ctx context.Context, root string, spawned time.Time) (time.Time, error) {
	if m.store == nil {
		return spawned, nil
	}
	return m.store.UserIntentBefore(ctx, root, spawned)
}

// PrepareSiblingNotes retains the last delivered page until new observations arrive.
func (m *Manager) PrepareSiblingNotes(ctx context.Context, child string, limit int) (inject.SiblingNotePage, error) {
	if m.findings == nil {
		return inject.SiblingNotePage{}, nil
	}
	job := workercontext.Job(ctx)
	if job == "" {
		return inject.SiblingNotePage{}, nil
	}
	prior, err := m.findings.Delivery(ctx, job)
	if err != nil {
		return inject.SiblingNotePage{}, err
	}
	notes, cursor, err := m.RecentSiblingNotes(ctx, child, prior.Cursor, limit+1)
	if err != nil {
		return inject.SiblingNotePage{}, err
	}
	more := false
	// Deliver the oldest unread page before retaining recent context.
	used := 0
	for i, note := range notes {
		size := len(note.Summary) + len(note.Ref) + len(note.Agent) + 128
		if i >= limit || used+size > 4096 {
			more = true
			notes = notes[:i]
			cursor = prior.Cursor
			if i > 0 {
				cursor = notes[i-1].ID
			}
			break
		}
		used += size
	}
	retained := make([]inject.SiblingNote, 0, len(prior.Notes))
	for i := len(prior.Notes) - 1; i >= 0 && len(retained)+len(notes) < limit; i-- {
		f := prior.Notes[i]
		size := len(f.Summary) + len(f.Ref) + len(f.Agent) + 128
		if used+size > 4096 {
			break
		}
		retained = append(retained, inject.SiblingNote{ID: f.ID, Agent: f.Agent, Summary: f.Summary, Ref: f.Ref, HasBody: f.HasBody})
		used += size
	}
	for i, j := 0, len(retained)-1; i < j; i, j = i+1, j-1 {
		retained[i], retained[j] = retained[j], retained[i]
	}
	notes = append(retained, notes...)
	if m.peerRejections != nil {
		for _, note := range m.peerRejections.RecentForRoot(RootSessionID(ctx, m.store, child), limit-len(notes)) {
			size := len(note.Summary) + len(note.Ref) + len(note.Agent) + 128
			if used+size > 4096 {
				break
			}
			notes = append(notes, note)
			used += size
		}
	}
	return inject.SiblingNotePage{Notes: notes, Cursor: cursor, More: more}, nil
}

// CommitSiblingNotes records delivery after the model response succeeds.
func (m *Manager) CommitSiblingNotes(ctx context.Context, child, response string, cursor int64, notes []inject.SiblingNote) error {
	if m.findings == nil || workercontext.Job(ctx) == "" {
		return nil
	}
	d := findings.Delivery{Cursor: cursor}
	for _, n := range notes {
		if n.ID == 0 {
			continue
		}
		f := findings.Finding{ID: n.ID, Agent: n.Agent, Summary: n.Summary, Ref: n.Ref, HasBody: n.HasBody}
		d.Notes = append(d.Notes, f)
	}
	return m.findings.CommitDelivery(ctx, workercontext.Job(ctx), response, d)
}
