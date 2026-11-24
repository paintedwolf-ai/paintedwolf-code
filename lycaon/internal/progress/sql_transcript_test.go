package progress_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	api "github.com/lycaon/lycaon/pkg/api"
)

type progressFixture struct {
	database *db.Store
	sessions *sessionstore.Repositories
	plans    *progress.SQLStore
	path     string
}

func newProgressFixture(t *testing.T) progressFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	database := testdbfixture.OpenPath(t, path)
	testdbseed.InsertWorkflowRun(t, database, "run", "session", testdbseed.DefaultProjectID)
	sessions := sessionstore.NewSQL(database)
	sessions.SetEventOutbox(eventoutbox.New(database, events.NewMemoryHub()))
	return progressFixture{database, sessions, progress.NewSQLStore(database, sessions), path}
}

func (f progressFixture) messages(t *testing.T) []api.Message {
	t.Helper()
	rows, err := f.sessions.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "read progress transcript", err)
	return rows
}

func replacePlan(t *testing.T, store progress.Store, specs ...progress.RowSpec) progress.Snapshot {
	t.Helper()
	res, err := store.Apply(t.Context(), "session", progress.Mutation{Replace: specs}, progress.ApplyOptions{})
	testutil.FailErr(t, "replace plan", err)
	return res.Snapshot
}

func updatePlan(t *testing.T, store progress.Store, updates ...progress.UpdateRowSpec) progress.Snapshot {
	t.Helper()
	res, err := store.Apply(t.Context(), "session", progress.Mutation{Update: updates}, progress.ApplyOptions{})
	testutil.FailErr(t, "update plan", err)
	return res.Snapshot
}

func TestPlanRowsCommitWithTransitionsAndSurviveRestart(t *testing.T) {
	f := newProgressFixture(t)
	testutil.FailErr(t, "append assistant", f.sessions.AppendMessages(t.Context(), "session", api.Message{ID: "assistant", Role: api.MessageRoleAssistant, Content: "Working", Visibility: api.MessageVisibilityTranscript}))

	replacePlan(t, f.plans, progress.RowSpec{Label: "Deliver", State: progress.ProgressStatePending})
	done := progress.ProgressStateDone
	pending := progress.ProgressStatePending
	renamed := "Renamed"
	steps := []struct {
		update progress.UpdateRowSpec
		kind   api.MessageKind
	}{
		{progress.UpdateRowSpec{ID: "p1", State: &done}, api.MessageKindProgressComplete},
		{progress.UpdateRowSpec{ID: "p1", State: &pending}, api.MessageKindProgressUpdate},
		{progress.UpdateRowSpec{ID: "p1", State: &done}, api.MessageKindProgressComplete},
		{progress.UpdateRowSpec{ID: "p1", Label: &renamed}, api.MessageKindProgressUpdate},
	}
	for i, step := range steps {
		updatePlan(t, f.plans, step.update)
		rows := f.messages(t)
		if len(rows) != i+3 {
			t.Fatalf("transition %d rows=%d", i, len(rows))
		}
		row := rows[len(rows)-1]
		if row.Kind != step.kind || row.WorkflowRunID != "run" || row.Ord != int64(i+3) || row.Seq <= 0 {
			t.Fatalf("transition %d row=%+v", i, row)
		}
		// Repeating the exact patch is a no-op.
		updatePlan(t, f.plans, step.update)
		if len(f.messages(t)) != len(rows) {
			t.Fatal("no-op duplicated a row")
		}
	}

	testutil.FailErr(t, "close committed store", f.database.Close())
	reopened := testdbfixture.OpenPath(t, f.path)
	sessions := sessionstore.NewSQL(reopened)
	plans := progress.NewSQLStore(reopened, sessions)
	_, err := plans.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "reopen plan", err)
	updatePlan(t, plans, progress.UpdateRowSpec{ID: "p1", Label: &renamed})
	rows, err := sessions.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "reopen transcript", err)
	if len(rows) != 6 {
		t.Fatalf("reopen rows=%d", len(rows))
	}

	hub := events.NewMemoryHub()
	stream, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe after restart", err)
	defer unsubscribe()
	outbox := eventoutbox.New(reopened, hub)
	outbox.Start(t.Context())
	defer func() { testutil.FailErr(t, "stop outbox", outbox.Close()) }()
	delivered := 0
	for delivered < len(rows) {
		select {
		case envelope := <-stream:
			if envelope.Topic != api.EventTopicMessage {
				continue
			}
			var event api.MessageEvent
			testutil.FailErr(t, "decode replayed event", json.Unmarshal(envelope.Data, &event))
			if event.Op != api.MessageChangeAppend {
				continue
			}
			if event.Message.ID != rows[delivered].ID || event.Message.Ord != rows[delivered].Ord || event.Message.Seq != rows[delivered].Seq {
				t.Fatalf("event %d diverged from stored row", delivered)
			}
			delivered++
		case <-t.Context().Done():
			t.Fatal("outbox delivery interrupted")
		}
	}
}

func TestPendingPlanCompletesAfterRestart(t *testing.T) {
	f := newProgressFixture(t)
	replacePlan(t, f.plans, progress.RowSpec{Label: "Deliver", State: progress.ProgressStatePending})
	testutil.FailErr(t, "close pending plan", f.database.Close())
	reopened := testdbfixture.OpenPath(t, f.path)
	sessions := sessionstore.NewSQL(reopened)
	plans := progress.NewSQLStore(reopened, sessions)
	done := progress.ProgressStateDone
	updatePlan(t, plans, progress.UpdateRowSpec{ID: "p1", State: &done})
	updatePlan(t, plans, progress.UpdateRowSpec{ID: "p1", State: &done})
	rows, err := sessions.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "read completed plan history", err)
	if len(rows) != 2 || rows[1].Kind != api.MessageKindProgressComplete || rows[1].WorkflowRunID != "run" || rows[1].Ord != 2 {
		t.Fatalf("completion after restart=%+v", rows)
	}
}

func TestDelayedNotificationsCannotReorderOrRelabelPlanRows(t *testing.T) {
	f := newProgressFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseObserver := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseObserver()
	detach := progress.RegisterWriteObserver(func(_ context.Context, event progress.WriteEvent) {
		if event.SessionID == "session" && event.After.DocumentVersion() == 1 {
			close(entered)
			<-release
		}
	})
	defer detach()
	doneCh := make(chan error, 1)
	go func() {
		_, err := f.plans.Apply(t.Context(), "session", progress.Mutation{
			Replace: []progress.RowSpec{{Label: "First", State: progress.ProgressStatePending}},
		}, progress.ApplyOptions{})
		doneCh <- err
	}()
	select {
	case <-entered:
	case err := <-doneCh:
		t.Fatalf("initial write failed: %v", err)
	}
	first := f.messages(t)
	if len(first) != 1 || first[0].WorkflowRunID != "run" {
		t.Fatalf("committed row missing before notification: %+v", first)
	}
	testutil.FailErr(t, "intervening append", f.sessions.AppendMessages(t.Context(), "session", api.Message{ID: "intervening", Role: api.MessageRoleAssistant, Content: "Next", Visibility: api.MessageVisibilityTranscript}))
	done := progress.ProgressStateDone
	updatePlan(t, f.plans, progress.UpdateRowSpec{ID: "p1", State: &done})
	_, err := f.database.ExecContext(t.Context(), `INSERT INTO workflow_runs (id,session_id,project_id,workflow_id,workflow_version,status,current_phase,created_at,updated_at,parent_run_id) SELECT 'child',session_id,project_id,workflow_id,workflow_version,status,current_phase,created_at,updated_at,id FROM workflow_runs WHERE id='run'`)
	testutil.FailErr(t, "insert child run", err)
	_, err = progress.AdoptActiveRun(t.Context(), f.plans, "session", "child")
	testutil.FailErr(t, "adopt child run", err)
	rows := f.messages(t)
	if len(rows) != 4 || rows[0].WorkflowRunID != "run" || rows[2].Kind != api.MessageKindProgressComplete || rows[2].WorkflowRunID != "run" || rows[3].WorkflowRunID != "child" {
		t.Fatalf("transition history=%+v", rows)
	}
	for i, row := range rows {
		if row.Ord != int64(i+1) {
			t.Fatalf("ordinal %d=%d", i, row.Ord)
		}
	}
	releaseObserver()
	testutil.FailErr(t, "finish delayed notification", <-doneCh)
	if len(f.messages(t)) != len(rows) {
		t.Fatal("notification duplicated history")
	}
}

func TestConcurrentPlanWritersUseEachCommittedPredecessor(t *testing.T) {
	f := newProgressFixture(t)
	replacePlan(t, f.plans, progress.RowSpec{Label: "Initial", State: progress.ProgressStatePending})
	const writers = 12
	start := make(chan struct{})
	results := make(chan error, writers)
	for i := 0; i < writers; i++ {
		idx := i + 1
		go func() {
			<-start
			label := fmt.Sprintf("Revision %d", idx)
			_, err := f.plans.Apply(t.Context(), "session", progress.Mutation{
				Update: []progress.UpdateRowSpec{{ID: "p1", Label: &label}},
			}, progress.ApplyOptions{})
			results <- err
		}()
	}
	close(start)
	for i := 0; i < writers; i++ {
		testutil.FailErr(t, "concurrent transition", <-results)
	}
	rows := f.messages(t)
	if len(rows) != writers+1 {
		t.Fatalf("concurrent rows=%d", len(rows))
	}
	prevLabel := "Initial"
	for i := 1; i < len(rows); i++ {
		changes := rows[i].ProgressUpdate.Changes
		if len(changes) != 1 || changes[0].PrevLabel != prevLabel {
			t.Fatalf("transition %d lost predecessor: got %+v, want prev=%q", i, changes, prevLabel)
		}
		prevLabel = changes[0].Label
	}
}

func TestCancellationAfterCommitRetainsThePlanAndRow(t *testing.T) {
	f := newProgressFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := progress.RegisterWriteObserver(func(_ context.Context, event progress.WriteEvent) {
		if event.SessionID == "session" {
			cancel()
		}
	})
	defer release()
	out, err := f.plans.Apply(ctx, "session", progress.Mutation{
		Replace: []progress.RowSpec{{Label: "Committed", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "return committed transition", err)
	if ctx.Err() == nil || len(out.Snapshot.Rows) != 1 || out.Snapshot.Rows[0].Label != "Committed" || len(f.messages(t)) != 1 {
		t.Fatal("post-commit cancellation changed the outcome")
	}
}
