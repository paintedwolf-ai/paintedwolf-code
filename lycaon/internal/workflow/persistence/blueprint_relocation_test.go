package persistence_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBlueprintRelocationAnnouncesEveryCommittedRevision(t *testing.T) {
	db := testdbfixture.Open(t, "relocation.db")
	store := persistence.New(db)
	ctx := t.Context()
	for _, id := range []string{"session-a", "session-b"} {
		testdbseed.InsertSessionWithRoot(t, db, id, testdbseed.DefaultProjectID, t.TempDir())
	}
	runs := []*api.WorkflowRun{
		{SessionID: "session-a", Status: api.WorkflowRunStatusRunning, BlueprintPath: "blueprints/old.md"},
		{SessionID: "session-a", Status: api.WorkflowRunStatusComplete, BlueprintPath: "blueprints/old.md"},
		{SessionID: "session-b", Status: api.WorkflowRunStatusPaused, BlueprintPath: "blueprints/old.md"},
		{SessionID: "session-b", Status: api.WorkflowRunStatusComplete, BlueprintPath: "blueprints/other.md"},
	}
	for _, run := range runs {
		if run.Status == api.WorkflowRunStatusComplete {
			completed := time.Now().UTC()
			run.CompletedAt = &completed
		}
		run.ProjectID = testdbseed.DefaultProjectID
		run.WorkflowID, run.WorkflowVersion, run.CurrentPhase = "plan", "1.0.0", "research"
		testutil.FailErr(t, "create run", store.State.CreateState(ctx, run, "", nil))
		_, err := db.ExecContext(ctx, "UPDATE workflow_runs SET review_revision = 7 WHERE id = ?", run.ID)
		testutil.FailErr(t, "seed review acceptance epoch", err)
	}
	store.Transactions.SetEventOutbox(eventoutbox.New(db, nil))
	sessions, err := store.Blueprints.RelocateBlueprintPath(ctx, testdbseed.DefaultProjectID, "blueprints/old.md", "blueprints/new.md")
	testutil.FailErr(t, "relocate blueprint", err)
	slices.Sort(sessions)
	if !slices.Equal(sessions, []string{"session-a", "session-b"}) {
		t.Fatalf("unique affected sessions = %v", sessions)
	}
	rows, err := db.QueryContext(ctx, "SELECT facet, entity_revision, data_json FROM event_outbox WHERE topic = 'workflow'")
	testutil.FailErr(t, "read relocation events", err)
	defer func() { testutil.FailErr(t, "close event rows", rows.Close()) }()
	seen := map[string]bool{}
	for rows.Next() {
		var id, raw string
		var revision int64
		testutil.FailErr(t, "read event", rows.Scan(&id, &revision, &raw))
		var event api.WorkflowEvent
		testutil.FailErr(t, "decode event", json.Unmarshal([]byte(raw), &event))
		if seen[id] || event.Event != api.WorkflowEventKindRunUpdated || event.WorkflowRunID != id || event.Run == nil || event.Run.Revision != revision || event.Run.BlueprintPath != "blueprints/new.md" {
			t.Fatalf("incorrect relocation event: facet=%s revision=%d event=%+v", id, revision, event)
		}
		seen[id] = true
	}
	testutil.FailErr(t, "iterate events", rows.Err())
	if len(seen) != 3 {
		t.Fatalf("relocation announced %d runs, want three", len(seen))
	}
	for i, prior := range runs {
		current, err := store.Runs.Get(ctx, prior.ID)
		testutil.FailErr(t, "read committed run", err)
		var reviewRevision int64
		testutil.FailErr(t, "read retained review acceptance epoch", db.QueryRowContext(ctx, "SELECT review_revision FROM workflow_runs WHERE id = ?", prior.ID).Scan(&reviewRevision))
		if reviewRevision != 7 {
			t.Fatalf("blueprint relocation changed review acceptance epoch: run=%s got=%d want=7", prior.ID, reviewRevision)
		}
		if i < 3 {
			if !seen[prior.ID] || current.Revision != prior.Revision+1 || current.BlueprintPath != "blueprints/new.md" {
				t.Fatalf("changed run not represented exactly: before=%+v after=%+v", prior, current)
			}
		} else if seen[prior.ID] || current.Revision != prior.Revision || current.BlueprintPath != prior.BlueprintPath {
			t.Fatalf("unmatched run changed: before=%+v after=%+v", prior, current)
		}
	}
	for _, from := range []string{"blueprints/old.md", "blueprints/new.md"} {
		sessions, err := store.Blueprints.RelocateBlueprintPath(ctx, testdbseed.DefaultProjectID, from, "blueprints/new.md")
		testutil.FailErr(t, "no-op relocation", err)
		if len(sessions) != 0 {
			t.Fatalf("no-op affected sessions: %v", sessions)
		}
	}
	var count int
	testutil.FailErr(t, "count final events", db.QueryRowContext(ctx, "SELECT COUNT(*) FROM event_outbox").Scan(&count))
	if count != 3 {
		t.Fatalf("no-op relocation added events: %d", count)
	}
}

func TestBlueprintRelocationRollsBackWhenEventCannotCommit(t *testing.T) {
	db := testdbfixture.Open(t, "relocation-rollback.db")
	testdbseed.InsertSessionWithRoot(t, db, "session", testdbseed.DefaultProjectID, t.TempDir())
	store := persistence.New(db)
	run := &api.WorkflowRun{SessionID: "session", ProjectID: testdbseed.DefaultProjectID, WorkflowID: "plan", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "research", BlueprintPath: "blueprints/old.md"}
	testutil.FailErr(t, "create run", store.State.CreateState(t.Context(), run, "", nil))
	store.Transactions.SetEventOutbox(eventoutbox.New(db, nil))
	_, err := db.ExecContext(t.Context(), "CREATE TRIGGER reject_relocation_event BEFORE INSERT ON event_outbox BEGIN SELECT RAISE(ABORT, 'fixture event failure'); END")
	testutil.FailErr(t, "install event failure", err)
	if _, err := store.Blueprints.RelocateBlueprintPath(t.Context(), run.ProjectID, run.BlueprintPath, "blueprints/new.md"); err == nil {
		t.Fatal("relocation committed without its event")
	}
	current, err := store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read rolled-back run", err)
	if current.Revision != run.Revision || current.BlueprintPath != run.BlueprintPath {
		t.Fatalf("failed event left a changed run: %+v", current)
	}
	var count int
	testutil.FailErr(t, "count rolled-back events", db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM event_outbox").Scan(&count))
	if count != 0 {
		t.Fatalf("failed relocation retained events: %d", count)
	}
}
