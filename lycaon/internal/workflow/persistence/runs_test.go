package persistence_test

import (
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestActiveByProjectForBlueprintScopesPathAndIncludesPausedParent(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := workflowpersistence.New(sqlDB)
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
		testutil.FailErr(t, "store.State.CreateState", store.State.CreateState(ctx, run, "", nil))
	}

	got, err := store.Runs.ActiveByProjectForBlueprint(ctx, projectA, path)
	testutil.FailErr(t, "ActiveByProjectForBlueprint", err)
	if got == nil || got.ProjectID != projectA || got.Status != api.WorkflowRunStatusPausedOnChild {
		t.Fatalf("run = %+v", got)
	}
}
func TestLatestChildRunBreaksTimestampTiesByAdmission(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "child-order.db")
	testdbseed.InsertSession(t, sqlDB, "session-order", testdbseed.DefaultProjectID)
	store := workflowpersistence.New(sqlDB)
	parent := &api.WorkflowRun{
		ID: "parent", SessionID: "session-order", WorkflowID: "parent", WorkflowVersion: "1",
		Status: api.WorkflowRunStatusPausedOnChild, CurrentPhase: "work",
	}
	testutil.FailErr(t, "create parent", store.State.CreateState(t.Context(), parent, "", nil))
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
		testutil.FailErr(t, "create child "+child.ID, store.State.CreateState(t.Context(), child, "", nil))
	}
	got, err := store.Runs.LatestChildByParentRunID(t.Context(), parent.ID)
	testutil.FailErr(t, "read current child", err)
	if got == nil || got.ID != newer.ID || got.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("latest child = %+v, want newer running child %s", got, newer.ID)
	}
}
func TestActiveBlueprintRunBreaksTimestampTiesByAdmission(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := workflowpersistence.New(sqlDB)
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
		testutil.FailErr(t, "create active blueprint run", store.State.CreateState(t.Context(), run, "", nil))
	}
	got, err := store.Runs.ActiveByProjectForBlueprint(t.Context(), testdbseed.DefaultProjectID, path)
	testutil.FailErr(t, "read active blueprint run", err)
	if got == nil || got.ID != ids[1] {
		t.Fatalf("active blueprint run = %+v, want %s", got, ids[1])
	}
}
