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
	sessions *sessionstore.SQL
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

func applyPlan(t *testing.T, store progress.Store, content string) progress.Snapshot {
	t.Helper()
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read predecessor", err)
	update, err := progress.PrepareAuthorUpdate(content)
	testutil.FailErr(t, "prepare plan", err)
	update = update.ExpectRevision(before.Revision)
	if len(before.ItemIDs) > 0 {
		update = update.ExpectItemIDs(before.ItemIDs...)
	}
	result, err := update.Apply(t.Context(), store, "session", nil)
	testutil.FailErr(t, "commit plan", err)
	return result.Snapshot
}

func TestPlanRowsCommitWithTransitionsAndSurviveRestart(t *testing.T) {
	f := newProgressFixture(t)
	testutil.FailErr(t, "append assistant", f.sessions.AppendMessages(t.Context(), "session", api.Message{ID: "assistant", Role: api.MessageRoleAssistant, Content: "Working", Visibility: api.MessageVisibilityTranscript}))
	for i, content := range []string{"- [ ] Deliver", "- [x] Deliver", "- [ ] Deliver", "- [x] Deliver", "- [x] Renamed"} {
		snapshot := applyPlan(t, f.plans, content)
		rows := f.messages(t)
		if len(rows) != i+2 {
			t.Fatalf("transition %d rows=%d", i, len(rows))
		}
		row := rows[len(rows)-1]
		expected := api.MessageKindProgressUpdate
		if i == 1 || i == 3 {
			expected = api.MessageKindProgressComplete
		}
		if row.Kind != expected || row.WorkflowRunID != "run" || row.Ord != int64(i+2) || row.Seq <= 0 {
			t.Fatalf("transition %d row=%+v", i, row)
		}
		applyPlan(t, f.plans, snapshot.Content)
		if len(f.messages(t)) != len(rows) {
			t.Fatal("no-op duplicated a row")
		}
	}
	testutil.FailErr(t, "close committed store", f.database.Close())
	reopened := testdbfixture.OpenPath(t, f.path)
	sessions := sessionstore.NewSQL(reopened)
	plans := progress.NewSQLStore(reopened, sessions)
	snapshot, err := plans.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "reopen plan", err)
	applyPlan(t, plans, snapshot.Content)
	rows, err := sessions.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "reopen transcript", err)
	if len(rows) != 6 {
		t.Fatalf("reopen rows=%d", len(rows))
	}
	// Delivery starts after reopening, without the original post-commit wake.
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
	applyPlan(t, f.plans, "- [ ] Deliver")
	testutil.FailErr(t, "close pending plan", f.database.Close())
	reopened := testdbfixture.OpenPath(t, f.path)
	sessions := sessionstore.NewSQL(reopened)
	plans := progress.NewSQLStore(reopened, sessions)
	applyPlan(t, plans, "- [x] Deliver")
	applyPlan(t, plans, "- [x] Deliver")
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
	done := make(chan error, 1)
	go func() { done <- f.plans.Set("session", "- [ ] First") }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("initial write failed: %v", err)
	}
	first := f.messages(t)
	if len(first) != 1 || first[0].WorkflowRunID != "run" {
		t.Fatalf("committed row missing before notification: %+v", first)
	}
	testutil.FailErr(t, "intervening append", f.sessions.AppendMessages(t.Context(), "session", api.Message{ID: "intervening", Role: api.MessageRoleAssistant, Content: "Next", Visibility: api.MessageVisibilityTranscript}))
	// The first operation has not returned; a later commit still keeps its own place.
	applyPlan(t, f.plans, "- [x] First")
	_, err := f.database.ExecContext(t.Context(), `INSERT INTO workflow_runs (id,session_id,project_id,workflow_id,workflow_version,status,current_phase,created_at,updated_at,parent_run_id) SELECT 'child',session_id,project_id,workflow_id,workflow_version,status,current_phase,created_at,updated_at,id FROM workflow_runs WHERE id='run'`)
	testutil.FailErr(t, "insert child run", err)
	_, err = progress.AdoptActiveRun(t.Context(), f.plans, "session", "child", "Next task")
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
	testutil.FailErr(t, "finish delayed notification", <-done)
	if len(f.messages(t)) != len(rows) {
		t.Fatal("notification duplicated history")
	}
}

func TestConcurrentPlanWritersUseEachCommittedPredecessor(t *testing.T) {
	f := newProgressFixture(t)
	const writers = 12
	start := make(chan struct{})
	results := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			<-start
			_, err := f.plans.Update(t.Context(), "session", func(before progress.Snapshot) (progress.Document, error) {
				return progress.Document{Content: fmt.Sprintf("- [ ] Revision %d", before.DocumentVersion()+1), ItemIDs: before.ItemIDs}, nil
			})
			results <- err
		}()
	}
	close(start)
	for i := 0; i < writers; i++ {
		testutil.FailErr(t, "concurrent transition", <-results)
	}
	rows := f.messages(t)
	if len(rows) != writers {
		t.Fatalf("concurrent rows=%d", len(rows))
	}
	for i, row := range rows {
		if row.Ord != int64(i+1) || row.ProgressUpdate == nil {
			t.Fatalf("row %d=%+v", i, row)
		}
		if i == 0 {
			if !row.ProgressUpdate.Initial || row.ProgressUpdate.Steps[0].Label != "Revision 1" {
				t.Fatalf("initial row=%+v", row)
			}
		} else {
			changes := row.ProgressUpdate.Changes
			if len(changes) != 1 || changes[0].PrevLabel != fmt.Sprintf("Revision %d", i) || changes[0].Label != fmt.Sprintf("Revision %d", i+1) {
				t.Fatalf("transition %d lost predecessor: %+v", i, changes)
			}
		}
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
	snapshot, err := f.plans.Update(ctx, "session", func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{Content: "- [ ] Committed"}, nil
	})
	testutil.FailErr(t, "return committed transition", err)
	if ctx.Err() == nil || snapshot.Content != "- [ ] Committed" || len(f.messages(t)) != 1 {
		t.Fatal("post-commit cancellation changed the outcome")
	}
}
