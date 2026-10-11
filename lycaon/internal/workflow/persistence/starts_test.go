package persistence_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestActivateStartRollsBackCancellationWhenInsertFails(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, sqlDB, "session-replace", testdbseed.DefaultProjectID)
	store := workflowpersistence.New(sqlDB)
	ctx := context.Background()
	active := &api.WorkflowRun{
		SessionID: "session-replace", WorkflowID: "first", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}
	testutil.FailErr(t, "CreateState", store.State.CreateState(ctx, active, "", nil))
	replacement := &api.WorkflowRun{
		ID: active.ID, SessionID: active.SessionID, WorkflowID: "second", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}
	mutation := runstate.StartMutation{Vars: map[string]any{}}
	if _, err := store.Starts.ActivateStart(ctx, "start-operation", "digest", active, replacement, mutation); err == nil {
		t.Fatal("ActivateStart succeeded with duplicate replacement id")
	}
	got, err := store.Runs.Get(ctx, active.ID)
	testutil.FailErr(t, "Get active", err)
	if got.Status != api.WorkflowRunStatusRunning || got.Revision != active.Revision {
		t.Fatalf("failed replacement changed active run: %+v", got)
	}
}
func TestWorkflowStartReplaysExactReceiptAndMutationsOutboxState(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	const sessionID = "session-exact-start"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	store := workflowpersistence.New(sqlDB)
	store.Transactions.SetEventOutbox(eventoutbox.New(sqlDB, nil))
	ctx := t.Context()
	run := &api.WorkflowRun{
		ID: "00000000-0000-4000-8000-000000000123", SessionID: sessionID,
		ProjectID: testdbseed.DefaultProjectID, WorkflowID: "plan", WorkflowVersion: "1",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "draft", Revision: 1,
	}
	_, err := store.Starts.ActivateStart(ctx, "operation-1", "digest-1", nil, run, runstate.StartMutation{Vars: map[string]any{}})
	testutil.FailErr(t, "ActivateStart", err)
	want := *run
	run.CurrentPhase = "review"
	run.UpdatedAt = time.Now().UTC()
	testutil.FailErr(t, "Update", store.State.Update(ctx, run))
	replayed, ok, err := store.Starts.ReplayStart(ctx, "operation-1", sessionID, "digest-1")
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
