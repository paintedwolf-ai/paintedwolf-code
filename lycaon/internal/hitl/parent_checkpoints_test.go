package hitl_test

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParentCheckpointQueryIncludesCompletedChildrenAndExcludesOtherChats(t *testing.T) {
	database := testdbfixture.Open(t, "parent-checkpoints.db")
	for _, id := range []string{"parent", "child", "other", "other-child"} {
		testdbseed.InsertSession(t, database, id, "p")
	}
	workers := worker.NewSQLStore(database)
	for _, task := range []api.WorkerTask{
		{ID: "job", ProjectID: "p", ParentSessionID: "parent", ChildSessionID: "child", Status: api.WorkerStatusComplete, AgentType: "implementer", Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal},
		{ID: "other-job", ProjectID: "p", ParentSessionID: "other", ChildSessionID: "other-child", Status: api.WorkerStatusRunning, AgentType: "implementer", Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal},
	} {
		testutil.FailErr(t, "insert worker", workers.InsertTask(t.Context(), task))
	}
	store := hitl.NewSQLStore(database)
	for _, id := range []string{"parent", "child", "other-child"} {
		testutil.FailErr(t, "insert checkpoint", store.Insert(t.Context(), hitl.StoredCheckpoint{ID: "cp-" + id, ProjectID: "p", SessionID: id, Kind: api.CheckpointKindToolApproval, Status: "pending", Type: hitl.DecisionTypeApprove, CreatedAt: time.Now().UTC()}))
	}
	rows, err := store.ListPendingForParent(t.Context(), "parent", nil)
	testutil.FailErr(t, "read parent pending checkpoints", err)
	if len(rows) != 2 || rows[0].SessionID != "parent" || rows[1].SessionID != "child" {
		t.Fatalf("parent scope = %+v", rows)
	}
	kind := api.CheckpointKindContentApply
	rows, err = store.ListPendingForParent(t.Context(), "parent", &kind)
	testutil.FailErr(t, "filter checkpoint kind", err)
	if len(rows) != 0 {
		t.Fatalf("kind filter = %+v", rows)
	}
}

func TestPendingCheckpointScopeAgeAndIsolation(t *testing.T) {
	database := testdbfixture.Open(t, "checkpoint-scope.db")
	for _, id := range []string{"parent", "child", "peer", "foreign-child"} {
		projectID := "project"
		if id == "foreign-child" {
			projectID = "foreign-project"
		}
		testdbseed.InsertSession(t, database, id, projectID)
	}
	workers := worker.NewSQLStore(database)
	for _, task := range []api.WorkerTask{
		{ID: "complete", ProjectID: "project", ParentSessionID: "parent", ChildSessionID: "child", Status: api.WorkerStatusComplete},
		{ID: "duplicate", ProjectID: "project", ParentSessionID: "parent", ChildSessionID: "child", Status: api.WorkerStatusRunning},
		{ID: "foreign", ProjectID: "project", ParentSessionID: "parent", ChildSessionID: "foreign-child", Status: api.WorkerStatusRunning},
	} {
		task.AgentType, task.Prompt, task.Brief = "implementer", "fixture", "fixture"
		task.ExecutionTarget = api.ExecutionTargetLocal
		testutil.FailErr(t, "insert worker", workers.InsertTask(t.Context(), task))
	}
	store := hitl.NewSQLStore(database)
	oldest := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"foreign-child", "child", "parent", "peer"} {
		projectID := "project"
		if id == "foreign-child" {
			projectID = "foreign-project"
		}
		testutil.FailErr(t, "insert checkpoint", store.Insert(t.Context(), hitl.StoredCheckpoint{
			ID: "cp-" + id, ProjectID: projectID, SessionID: id,
			Kind: api.CheckpointKindContentApply, Status: hitl.DecisionStatusPending,
			CreatedAt: oldest.Add(time.Duration(i) * time.Minute),
		}))
	}
	rows, err := store.ListPendingForParent(t.Context(), "parent", nil)
	testutil.FailErr(t, "read parent checkpoint scope", err)
	if len(rows) != 2 || rows[0].ID != "cp-child" || rows[1].ID != "cp-parent" {
		t.Fatalf("scope leaked or duplicated checkpoints: %+v", rows)
	}
	ages, err := store.OldestPendingBySession(t.Context())
	testutil.FailErr(t, "read scope ages", err)
	if !ages["parent"].Equal(oldest.Add(time.Minute)) {
		t.Fatalf("parent oldest = %s", ages["parent"])
	}
	_, err = database.ExecContext(t.Context(), `UPDATE checkpoints SET status = 'canceled' WHERE id = 'cp-child'`)
	testutil.FailErr(t, "cancel oldest checkpoint", err)
	ages, err = store.OldestPendingBySession(t.Context())
	testutil.FailErr(t, "read remaining scope ages", err)
	if !ages["parent"].Equal(oldest.Add(2 * time.Minute)) {
		t.Fatalf("parent oldest after child resolution = %s", ages["parent"])
	}
}
