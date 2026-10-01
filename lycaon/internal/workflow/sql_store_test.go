package workflow

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type countingRunnableNotifier struct{ calls int }

func (n *countingRunnableNotifier) NotifyRunnable() { n.calls++ }

func TestSQLStoreWorkflowRunCRUD(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	testdbseed.InsertSession(t, sqlDB, "sess-1", testdbseed.DefaultProjectID)
	run := &api.WorkflowRun{
		SessionID:       "sess-1",
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "research",
	}
	if err := store.CreateState(context.Background(), run, "", nil); err != nil {
		testutil.FailErr(t, "create session in store", err)
	}
	if run.ID == "" {
		t.Fatal("expected id assigned")
	}
	got, err := store.Get(context.Background(), run.ID)
	testutil.FailErr(t, "store.Get failed", err)
	if got.CurrentPhase != "research" {
		t.Fatalf("phase = %q", got.CurrentPhase)
	}
	if err := store.UpdateVars(context.Background(), run, "/tmp/p", map[string]any{"k": "v"}); err != nil {
		testutil.FailErr(t, "store.UpdateVars failed", err)
	}
}

func TestWorkflowReleaseWakesWorkerClaimerAfterCommit(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, sqlDB, "session-release", testdbseed.DefaultProjectID)
	store := NewSQLStore(sqlDB)
	notifier := &countingRunnableNotifier{}
	store.SetWorkerRunnableNotifier(notifier)
	run := &api.WorkflowRun{
		SessionID: "session-release", WorkflowID: "plan", WorkflowVersion: "1",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}
	testutil.FailErr(t, "create workflow run", store.CreateState(t.Context(), run, "", nil))
	run.UpdatedAt = time.Now().UTC()
	err := store.CommitCommand(t.Context(), run, workflowCommandMutation{
		OperationID: "release-operation", Kind: "resume", InputDigest: "release-digest",
		Workers: workflowWorkerMutation{ReleaseHeld: true},
	})
	testutil.FailErr(t, "commit workflow release", err)
	if notifier.calls != 1 {
		t.Fatalf("runnable notifications = %d want 1", notifier.calls)
	}
}

func TestActiveByProjectForBlueprintScopesPathAndIncludesPausedParent(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	ctx := t.Context()
	const (
		projectA = "00000000-0000-4000-8000-00000000000a"
		projectB = "00000000-0000-4000-8000-00000000000b"
	)
	path := settingsoverlay.Rel("blueprints/shared.md")
	testdbseed.InsertSession(t, sqlDB, "sess-a", projectA)
	testdbseed.InsertSession(t, sqlDB, "sess-b", projectB)
	for _, run := range []*api.WorkflowRun{
		{
			SessionID: "sess-a", ProjectID: projectA, WorkflowID: "plan", WorkflowVersion: "1.0.0",
			Status: api.WorkflowRunStatusPausedOnChild, CurrentPhase: "execute", BlueprintPath: path,
		},
		{
			SessionID: "sess-b", ProjectID: projectB, WorkflowID: "plan", WorkflowVersion: "1.0.0",
			Status: api.WorkflowRunStatusRunning, CurrentPhase: "approve", BlueprintPath: path,
		},
	} {
		testutil.FailErr(t, "store.CreateState", store.CreateState(ctx, run, "", nil))
	}

	got, err := store.ActiveByProjectForBlueprint(ctx, projectA, path)
	testutil.FailErr(t, "ActiveByProjectForBlueprint", err)
	if got == nil || got.ProjectID != projectA || got.Status != api.WorkflowRunStatusPausedOnChild {
		t.Fatalf("run = %+v", got)
	}
}

func TestWorkflowRunRevisionRejectsStaleLifecycleWrite(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, sqlDB, "session-revision", testdbseed.DefaultProjectID)
	store := NewSQLStore(sqlDB)
	ctx := context.Background()
	run := &api.WorkflowRun{
		SessionID: "session-revision", WorkflowID: "test", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}
	testutil.FailErr(t, "CreateState", store.CreateState(ctx, run, "", nil))
	first, err := store.Get(ctx, run.ID)
	testutil.FailErr(t, "Get first", err)
	stale, err := store.Get(ctx, run.ID)
	testutil.FailErr(t, "Get stale", err)

	first.Status = api.WorkflowRunStatusCanceled
	first.UpdatedAt = time.Now().UTC()
	first.CompletedAt = new(first.UpdatedAt)
	testutil.FailErr(t, "cancel", store.Update(ctx, first))
	stale.Status = api.WorkflowRunStatusPaused
	stale.UpdatedAt = time.Now().UTC()
	if err := store.Update(ctx, stale); !errors.Is(err, ErrRunRevisionConflict) {
		t.Fatalf("stale update err = %v want ErrRunRevisionConflict", err)
	}
	got, err := store.Get(ctx, run.ID)
	testutil.FailErr(t, "Get final", err)
	if got.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("stale write resurrected status %q", got.Status)
	}
}

func TestActivateStartRollsBackCancellationWhenInsertFails(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, sqlDB, "session-replace", testdbseed.DefaultProjectID)
	store := NewSQLStore(sqlDB)
	ctx := context.Background()
	active := &api.WorkflowRun{
		SessionID: "session-replace", WorkflowID: "first", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}
	testutil.FailErr(t, "CreateState", store.CreateState(ctx, active, "", nil))
	replacement := &api.WorkflowRun{
		ID: active.ID, SessionID: active.SessionID, WorkflowID: "second", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}
	mutation := workflowStartMutation{Vars: map[string]any{}}
	if _, err := store.ActivateStart(ctx, "start-operation", "digest", active, replacement, mutation); err == nil {
		t.Fatal("ActivateStart succeeded with duplicate replacement id")
	}
	got, err := store.Get(ctx, active.ID)
	testutil.FailErr(t, "Get active", err)
	if got.Status != api.WorkflowRunStatusRunning || got.Revision != active.Revision {
		t.Fatalf("failed replacement changed active run: %+v", got)
	}
}

func TestWorkflowStartReplaysExactReceiptAndMutationsOutboxState(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	const sessionID = "session-exact-start"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	store := NewSQLStore(sqlDB)
	store.SetEventOutbox(eventoutbox.New(sqlDB, nil))
	ctx := t.Context()
	run := &api.WorkflowRun{
		ID: "00000000-0000-4000-8000-000000000123", SessionID: sessionID,
		ProjectID: testdbseed.DefaultProjectID, WorkflowID: "plan", WorkflowVersion: "1",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "draft", Revision: 1,
	}
	_, err := store.ActivateStart(ctx, "operation-1", "digest-1", nil, run, workflowStartMutation{Vars: map[string]any{}})
	testutil.FailErr(t, "ActivateStart", err)
	want := *run
	run.CurrentPhase = "review"
	run.UpdatedAt = time.Now().UTC()
	testutil.FailErr(t, "Update", store.Update(ctx, run))
	replayed, ok, err := store.ReplayStart(ctx, "operation-1", sessionID, "digest-1")
	testutil.FailErr(t, "ReplayStart", err)
	if !ok || !reflect.DeepEqual(replayed, &want) {
		t.Fatalf("ReplayStart = %+v ok=%v, want exact %+v", replayed, ok, &want)
	}
	var eventCount int
	testutil.FailErr(t, "count workflow outbox", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event_outbox WHERE topic = 'workflow' AND session_id = ?`, sessionID).Scan(&eventCount))
	if eventCount != 2 {
		t.Fatalf("workflow outbox count = %d, want one row per committed mutation", eventCount)
	}
}

func TestManagerDoesNotDuplicateOutboxedWorkflowMutation(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	hub := events.NewMemoryHub()
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsubscribe)
	store := NewSQLStore(sqlDB)
	store.SetEventOutbox(eventoutbox.New(sqlDB, hub))
	manager := &RunManager{Store: store, Events: &events.Publisher{Hub: hub}}
	manager.publishSession(t.Context(), &api.WorkflowRun{
		ID: "run-1", SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "plan", Status: api.WorkflowRunStatusRunning, Revision: 1,
	})
	select {
	case event := <-ch:
		t.Fatalf("manager emitted second workflow event outside mutation transaction: %+v", event)
	default:
	}
}

func TestLatestChildRunBreaksTimestampTiesByAdmission(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "child-order.db")
	testdbseed.InsertSession(t, sqlDB, "session-order", testdbseed.DefaultProjectID)
	store := NewSQLStore(sqlDB)
	parent := &api.WorkflowRun{
		ID: "parent", SessionID: "session-order", WorkflowID: "parent", WorkflowVersion: "1",
		Status: api.WorkflowRunStatusPausedOnChild, CurrentPhase: "work",
	}
	testutil.FailErr(t, "create parent", store.CreateState(t.Context(), parent, "", nil))
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	older := &api.WorkflowRun{
		ID: "ffffffff-ffff-4fff-8fff-ffffffffffff", SessionID: parent.SessionID, ParentRunID: &parent.ID,
		WorkflowID: "child", WorkflowVersion: "1", Status: api.WorkflowRunStatusComplete,
		CurrentPhase: "done", CreatedAt: at, CompletedAt: &at,
	}
	newer := &api.WorkflowRun{
		ID: "00000000-0000-4000-8000-000000000001", SessionID: parent.SessionID, ParentRunID: &parent.ID,
		WorkflowID: "child", WorkflowVersion: "1", Status: api.WorkflowRunStatusRunning,
		CurrentPhase: "work", CreatedAt: at,
	}
	for _, child := range []*api.WorkflowRun{older, newer} {
		testutil.FailErr(t, "create child "+child.ID, store.CreateState(t.Context(), child, "", nil))
	}
	got, err := store.LatestChildByParentRunID(t.Context(), parent.ID)
	testutil.FailErr(t, "read current child", err)
	if got == nil || got.ID != newer.ID || got.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("latest child = %+v, want newer running child %s", got, newer.ID)
	}
}

func TestActiveBlueprintRunBreaksTimestampTiesByAdmission(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := NewSQLStore(sqlDB)
	path := settingsoverlay.Rel("blueprints/shared.md")
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	ids := []string{"ffffffff-ffff-4fff-8fff-ffffffffffff", "00000000-0000-4000-8000-000000000001"}
	for _, id := range ids {
		testdbseed.InsertSession(t, sqlDB, "session-"+id, testdbseed.DefaultProjectID)
		run := &api.WorkflowRun{
			ID: id, SessionID: "session-" + id, ProjectID: testdbseed.DefaultProjectID,
			WorkflowID: "plan", WorkflowVersion: "1", BlueprintPath: path,
			Status: api.WorkflowRunStatusRunning, CurrentPhase: "work", CreatedAt: at,
		}
		testutil.FailErr(t, "create active blueprint run", store.CreateState(t.Context(), run, "", nil))
	}
	got, err := store.ActiveByProjectForBlueprint(t.Context(), testdbseed.DefaultProjectID, path)
	testutil.FailErr(t, "read active blueprint run", err)
	if got == nil || got.ID != ids[1] {
		t.Fatalf("active blueprint run = %+v, want %s", got, ids[1])
	}
}
