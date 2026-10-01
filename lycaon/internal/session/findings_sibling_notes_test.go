package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubSiblingQueue struct {
	noopWorkerBranchClaim
	byChild map[string]string // childSessionID -> jobID
}

func (s *stubSiblingQueue) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func (s *stubSiblingQueue) Get(jobID string) (*api.WorkerTask, bool) {
	for _, id := range s.byChild {
		if id == jobID {
			return &api.WorkerTask{ID: id}, true
		}
	}
	return nil, false
}

func TestRecentSiblingNotesExcludesOwnAndAdvancesCursor(t *testing.T) {
	// The child is its own root.
	root := "child-self"
	store := findings.NewMemoryStore()
	if _, err := store.Append(context.Background(), root, "job-a", "config in resolve.go:40", "resolve.go:40", ""); err != nil {
		t.Fatalf("append peer finding: %v", err)
	}
	if _, err := store.Append(context.Background(), root, "job-self", "my own note", "", ""); err != nil {
		t.Fatalf("append own finding: %v", err)
	}

	m := &Manager{
		workerQueue: &stubSiblingQueue{byChild: map[string]string{"child-self": "job-self"}},
		findings:    store,
	}

	ctx := workercontext.WithJob(context.Background(), "job-self")
	notes, cursor, err := m.RecentSiblingNotes(ctx, "child-self", 0, 5)
	if err != nil {
		t.Fatalf("load notes: %v", err)
	}
	if len(notes) != 1 || notes[0].Agent != "job-a" || notes[0].Ref != "resolve.go:40" {
		t.Fatalf("notes = %+v", notes)
	}

	notes2, nextCursor, err := m.RecentSiblingNotes(ctx, "child-self", cursor, 5)
	if err != nil {
		t.Fatalf("reload notes: %v", err)
	}
	if len(notes2) != 0 {
		t.Fatalf("expected no repeat from advanced cursor, got %+v", notes2)
	}

	if _, err := store.Append(context.Background(), root, "job-b", "use the lexer cache", "", ""); err != nil {
		t.Fatalf("append new finding: %v", err)
	}
	notes3, _, err := m.RecentSiblingNotes(ctx, "child-self", nextCursor, 5)
	if err != nil {
		t.Fatalf("load new notes: %v", err)
	}
	if len(notes3) != 1 || notes3[0].Agent != "job-b" {
		t.Fatalf("notes3 = %+v", notes3)
	}
}

func (*stubSiblingQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}
