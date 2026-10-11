package publication

import (
	"context"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type publicationSessions struct{ rows []api.Message }

func (s *publicationSessions) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ProjectID: "project"}, nil
}
func (s *publicationSessions) AppendMessages(_ context.Context, _ string, msgs ...api.Message) error {
	s.rows = append(s.rows, msgs...)
	return nil
}
func (*publicationSessions) MutationEventsOutboxed() bool { return true }

type publicationRuns struct{ runstate.RunsRepository }

func (publicationRuns) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return &api.WorkflowRun{ID: "active"}, nil
}

func TestWorkflowStampPreservesExplicitRunAndBoundaryMessages(t *testing.T) {
	sessions := &publicationSessions{}
	publisher := &Messages{Sessions: sessions, Runs: publicationRuns{}}
	err := publisher.StampAndAppendMessages(t.Context(), "session", api.Message{ID: "ordinary"}, api.Message{ID: "explicit", WorkflowRunID: "previous"}, api.Message{ID: "boundary", Kind: api.MessageKindWorkflowBoundary})
	if err != nil {
		t.Fatalf("operation failed: %v", err)
	}
	if len(sessions.rows) != 3 || sessions.rows[0].WorkflowRunID != "active" || sessions.rows[1].WorkflowRunID != "previous" || sessions.rows[2].WorkflowRunID != "" {
		t.Fatalf("stamped messages=%+v", sessions.rows)
	}
}
