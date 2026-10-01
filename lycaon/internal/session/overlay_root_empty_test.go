package session_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkspaceRootEmptyWithoutBranch(t *testing.T) {
	task := &api.WorkerTask{
		WorkspacePath: "/primary",
		WorkspaceRoot: "",
	}
	if got := strings.TrimSpace(task.WorkspaceRoot); got != "" {
		t.Fatalf("WorkspaceRoot=%q want empty (not primary)", got)
	}
	if task.PrimaryRootPath() != "/primary" {
		t.Fatalf("PrimaryRootPath=%q", task.PrimaryRootPath())
	}
}
