package instructions

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
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
	database := testdbfixture.Open(t, "progress.db")
	testdbseed.InsertWorkflowRun(t, database, "run-0", "sess-1", testdbseed.DefaultProjectID)
	store := progress.NewSQLStore(database, sessionstore.NewSQL(database))
	seeded := progresstest.SeedRows(t, store, "sess-1", progress.RowSpec{Label: "Initial run"})
	progresstest.UpdateRows(t, store, "sess-1", progress.UpdateRowSpec{ID: seeded.Rows[0].ID, State: progresstest.State("done")})

	_, err := database.ExecContext(t.Context(), `UPDATE workflow_runs SET status = 'complete', completed_at = updated_at WHERE id = 'run-0'`)
	if err != nil {
		t.Fatalf("mark prior run complete: %v", err)
	}
	testdbseed.InsertWorkflowRun(t, database, "run-1", "sess-1", testdbseed.DefaultProjectID)

	svc := New(stubInstructionStore{session: &api.Session{ID: "sess-1"}}, stubInstructionStore{session: &api.Session{ID: "sess-1"}}, nil, nil, nil)
	svc.SetProgress(store)
	svc.SetWorkflow(&WorkflowDomains{Runs: stubWorkflowRuns{run: &api.WorkflowRun{ID: "run-1"}}})

	var events []progress.WriteEvent
	release := progress.RegisterWriteObserver(func(_ context.Context, ev progress.WriteEvent) {
		if ev.SessionID == "sess-1" {
			events = append(events, ev)
		}
	})
	defer release()

	if err := svc.BootstrapProgress(context.Background(), "sess-1", api.Message{
		Role:    "user",
		Origin:  api.MessageOriginUser,
		Content: "Ship stable progress item identities",
	}); err != nil {
		t.Fatalf("BootstrapProgress: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Before.RunID != "run-0" || len(events[0].Before.Rows) != 1 {
		t.Fatalf("expected run-0 before snapshot, got %+v", events[0].Before)
	}
	if events[0].After.RunID != "run-1" || len(events[0].After.Rows) != 0 {
		t.Fatalf("expected cleared run-1 after snapshot, got %+v", events[0].After)
	}
}
