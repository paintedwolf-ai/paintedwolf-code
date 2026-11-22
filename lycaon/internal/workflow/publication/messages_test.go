package publication

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type publicationSessions struct {
	rows     []api.Message
	outboxed bool
}

func (s *publicationSessions) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ProjectID: "project"}, nil
}
func (s *publicationSessions) AppendMessages(_ context.Context, _ string, msgs ...api.Message) error {
	s.rows = append(s.rows, msgs...)
	return nil
}
func (s *publicationSessions) MessageEventsOutboxed() bool { return s.outboxed }

type publicationRuns struct{ runstate.RunsRepository }

func (publicationRuns) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return &api.WorkflowRun{ID: "active"}, nil
}

func TestWorkflowStampPreservesExplicitRunAndBoundaryMessages(t *testing.T) {
	sessions := &publicationSessions{outboxed: true}
	publisher := &Messages{SessionStore: sessions, TranscriptStore: sessions, Runs: publicationRuns{}, Events: &events.Publisher{Hub: events.NewMemoryHub()}}
	err := publisher.StampAndAppendMessages(t.Context(), "session", api.Message{ID: "ordinary"}, api.Message{ID: "explicit", WorkflowRunID: "previous"}, api.Message{ID: "boundary", Kind: api.MessageKindWorkflowBoundary})
	if err != nil {
		t.Fatalf("operation failed: %v", err)
	}
	if len(sessions.rows) != 3 || sessions.rows[0].WorkflowRunID != "active" || sessions.rows[1].WorkflowRunID != "previous" || sessions.rows[2].WorkflowRunID != "" {
		t.Fatalf("stamped messages=%+v", sessions.rows)
	}
	if err := publisher.Append(t.Context(), "session", api.Message{ID: "appended"}); err != nil {
		t.Fatalf("append failed: %v", err)
	}
	publisher.PublishPatch(t.Context(), "session", api.Message{ID: "appended"})
	sessions.outboxed = false
	if err := publisher.Append(t.Context(), "session", api.Message{ID: "live"}); err != nil {
		t.Fatalf("live append failed: %v", err)
	}
	publisher.PublishPatch(t.Context(), "session", api.Message{ID: "live"})
	if err := (&Messages{}).Append(t.Context(), "session", api.Message{ID: "missing"}); err == nil {
		t.Fatal("expected unconfigured store error on Append")
	}
	if err := (&Messages{}).StampAndAppendMessages(t.Context(), "session", api.Message{ID: "missing"}); err == nil {
		t.Fatal("expected unconfigured store error on StampAndAppendMessages")
	}
}
