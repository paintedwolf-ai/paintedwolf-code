package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromoteExcludesUnchangedSiblingPath(t *testing.T) {
	ctx := context.Background()
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-main")

	mustWrite := func(root, rel, content string) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "MkdirAll "+rel, os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "WriteFile "+rel, os.WriteFile(abs, []byte(content), 0o644))
	}

	const (
		mainNew        = "import arcade\n\nclass Game:\n    pass\n"
		constantsFixed = "SCREEN_WIDTH = 1200\nSCREEN_HEIGHT = 900\nFPS = 60\n"
		constantsStale = "SCREEN_WIDTH = 1200\nSCREEN_HEIGHT = 900\n"
	)

	mustWrite(primary, "space_pacifism/constants.py", constantsStale)
	mustWrite(overlay, "space_pacifism/constants.py", constantsStale)
	baseline := testbaseline.Capture(t, overlay)

	mustWrite(overlay, "space_pacifism/main.py", mainNew)
	mustWrite(primary, "space_pacifism/constants.py", constantsFixed)

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"space_pacifism/main.py"}}

	task := &api.WorkerTask{
		AgentType:             "implementer",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         overlay,
		ChildSessionID:        "child-1",
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}

	deps := ChangeReportDeps{
		Messages: func(context.Context, string) ([]api.Message, error) {
			return []api.Message{
				{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "tc1", Name: "write", Args: map[string]any{"path": "space_pacifism/main.py"}}}},
				{Role: api.MessageRoleTool, Content: `{"ok":true}`},
			}, nil
		},
	}

	report := BuildChangeReport(ctx, *task, deps)
	if len(report.ChangedPaths) != 1 || report.ChangedPaths[0] != "space_pacifism/main.py" {
		t.Fatalf("ChangedPaths = %v, want [space_pacifism/main.py]", report.ChangedPaths)
	}

	assessment, err := AssessPromotePaths3Way(ctx, nil, "sess", task, report.ChangedPaths)
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(assessment.Conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", assessment.Conflicts)
	}
	if len(assessment.CleanPaths) != 1 || assessment.CleanPaths[0] != "space_pacifism/main.py" {
		t.Fatalf("CleanPaths = %v, want [space_pacifism/main.py]", assessment.CleanPaths)
	}
}
