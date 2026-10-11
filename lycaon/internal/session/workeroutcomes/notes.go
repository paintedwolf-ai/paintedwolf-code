package workeroutcomes

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/findings"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

type SessionHistory interface {
	Get(context.Context, string) (*api.Session, error)
	UserIntentBefore(context.Context, string, time.Time) (time.Time, error)
}

type FindingHistory interface {
	Delivery(context.Context, string) (findings.Delivery, error)
	CommitDelivery(context.Context, string, string, findings.Delivery) error
	Recent(context.Context, string, string, int64, int, time.Time) ([]findings.Finding, int64, error)
	List(context.Context, string, int) ([]findings.Finding, error)
}

type TaskReader interface {
	Get(string) (*api.WorkerTask, bool)
}
type PeerNotes interface {
	RecentForRoot(string, int) []inject.SiblingNote
}

type Notes struct {
	sessions SessionHistory
	findings FindingHistory
	workers  TaskReader
	peers    PeerNotes
}

func NewNotes(sessions SessionHistory, findings FindingHistory, workers TaskReader, peers PeerNotes) *Notes {
	return &Notes{sessions: sessions, findings: findings, workers: workers, peers: peers}
}
func (m *Notes) SetFindings(findings FindingHistory) { m.findings = findings }
func (m *Notes) SetWorkers(workers TaskReader)       { m.workers = workers }
func (m *Notes) SetPeers(peers PeerNotes)            { m.peers = peers }

// findingsBoundary includes agreements published before a sibling started.
func (m *Notes) findingsBoundary(ctx context.Context, root string, spawned time.Time) (time.Time, error) {
	if m.sessions == nil {
		return spawned, nil
	}
	return m.sessions.UserIntentBefore(ctx, root, spawned)
}

// PrepareSiblingNotes retains the last delivered page until new observations arrive.
func (m *Notes) PrepareSiblingNotes(ctx context.Context, child string, limit int) (inject.SiblingNotePage, error) {
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
	if m.peers != nil {
		for _, note := range m.peers.RecentForRoot(sessiontree.RootID(ctx, m.sessions, child), limit-len(notes)) {
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
func (m *Notes) CommitSiblingNotes(ctx context.Context, child, response string, cursor int64, notes []inject.SiblingNote) error {
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

// RecentSiblingNotes returns fresh findings from sibling workers.
func (m *Notes) RecentSiblingNotes(ctx context.Context, childSessionID string, afterID int64, max int) ([]inject.SiblingNote, int64, error) {
	if m == nil || m.findings == nil || strings.TrimSpace(childSessionID) == "" {
		return nil, afterID, nil
	}
	root := sessiontree.RootID(ctx, m.sessions, childSessionID)
	if strings.TrimSpace(root) == "" {
		return nil, afterID, nil
	}
	ownJob := ""
	batchStart := time.Time{}
	if m.workers != nil {
		if task, ok := m.workers.Get(workercontext.Job(ctx)); ok && task != nil {
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
func (m *Notes) ListFindings(ctx context.Context, rootSessionID string, max int) ([]findings.Finding, error) {
	if m == nil || m.findings == nil {
		return nil, nil
	}
	return m.findings.List(ctx, rootSessionID, max)
}
