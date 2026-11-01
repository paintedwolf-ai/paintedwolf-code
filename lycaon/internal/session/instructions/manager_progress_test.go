package instructions

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubInstructionStore struct {
	session *api.Session
}

func (s stubInstructionStore) Get(context.Context, string) (*api.Session, error) {
	return s.session, nil
}

func (s stubInstructionStore) ReadSourceTurn(context.Context, string, int) (messageview.SourceTurn, error) {
	return messageview.SourceTurn{}, nil
}

type stubWorkflowRuns struct {
	run *api.WorkflowRun
}

func (r stubWorkflowRuns) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return r.run, nil
}

func TestBootstrapProgressNotifiesWithBeforeAndAfterSnapshots(t *testing.T) {
	store := progress.NewMemoryStore()
	store.EnsureRun("sess-1", "run-0", "Initial run")
	svc := New(stubInstructionStore{session: &api.Session{ID: "sess-1"}}, nil, nil, nil)
	svc.SetProgress(store)
	svc.SetWorkflow(&WorkflowDomains{Runs: stubWorkflowRuns{run: &api.WorkflowRun{ID: "run-1"}}})

	var events []progress.WriteEvent
	release := progress.RegisterWriteObserver(func(_ context.Context, ev progress.WriteEvent) {
		if ev.SessionID == "sess-1" {
			events = append(events, ev)
		}
	})
	defer release()

	svc.BootstrapProgress(context.Background(), "sess-1", api.Message{
		Role:    "user",
		Origin:  api.MessageOriginUser,
		Content: "Ship stable progress item identities",
	})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Before.RunID != "run-0" || events[0].Before.Revision == "" {
		t.Fatalf("expected run-0 before snapshot, got %+v", events[0].Before)
	}
	if events[0].After.RunID != "run-1" || events[0].After.Revision == "" {
		t.Fatalf("expected run-1 after snapshot, got %+v", events[0].After)
	}
}
