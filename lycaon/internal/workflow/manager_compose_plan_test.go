package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestComposedWorkflowStartLinksPlanDraft(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	run, err := mgr.Starts.StartHuman(context.Background(), sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "mgr.StartHuman failed", err)
	if run.BlueprintPath == "" {
		t.Fatal("expected draft plan id on plan workflow run")
	}
	p, err := mgr.Blueprints.Getter.Get(context.Background(), run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "mgr.Blueprints.Getter.Get failed", err)
	if p.Title != "blueprint" {
		t.Fatalf("plan title = %q want blueprint", p.Title)
	}
	if p.Path == settingsoverlay.DirName()+"/blueprints/plan.md" {
		t.Fatalf("composed start must mint path, got %q", p.Path)
	}
}
