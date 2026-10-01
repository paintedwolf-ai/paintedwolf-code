package worker

import (
	"context"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestDraftScratchEnqueuePersistsWorkspaceRootID(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	rootID, scratch := testdbseed.InsertDraftScratchRoot(t, sqlDB, testdbseed.DefaultProjectID)

	task := api.WorkerTask{
		ID:              "job-draft-1",
		AgentType:       "implementer",
		Prompt:          "fixture",
		Brief:           "fixture",
		Scope:           &api.TaskScope{Mode: "write", Paths: []string{"src"}},
		WorkspaceRootID: rootID,
		WorkspacePath:   scratch,
		Status:          api.WorkerStatusPending,
		ExecutionTarget: api.ExecutionTargetLocal,
	}
	scope := project.ProjectScope{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspaceRootID: rootID,
		WorkspacePath:   scratch,
		HasRoots:        true,
	}
	if err := ApplyEnqueueDefaults(&task, scope, DefaultWorkersConfig()); err != nil {
		testutil.FailErr(t, "ApplyEnqueueDefaults", err)
	}
	if task.WorkspaceRootID != rootID {
		t.Fatalf("workspace_root_id = %q want %q", task.WorkspaceRootID, rootID)
	}
	if err := store.InsertTask(context.Background(), task); err != nil {
		testutil.FailErr(t, "InsertTask", err)
	}
}
