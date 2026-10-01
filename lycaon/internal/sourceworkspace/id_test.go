package sourceworkspace

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func TestIDIsStableAcrossRootOrder(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	a := ID("project-1", []projectroot.RootRef{{ID: "r1", Path: first}, {ID: "r2", Path: second}})
	b := ID("project-1", []projectroot.RootRef{{ID: "r2", Path: second}, {ID: "r1", Path: first}})
	if a == "" || a != b {
		t.Fatalf("workspace ids = %q and %q, want one stable non-empty id", a, b)
	}
}

func TestIDChangesWithPhysicalResolution(t *testing.T) {
	root := t.TempDir()
	a := ID("project-1", []projectroot.RootRef{{ID: "r1", Path: root}})
	b := ID("project-1", []projectroot.RootRef{{ID: "r1", Path: filepath.Join(root, "worktree")}})
	if a == b {
		t.Fatalf("workspace id did not change with root path: %q", a)
	}
	if ID("", []projectroot.RootRef{{ID: "r1", Path: root}}) != "" || ID("project-1", nil) != "" {
		t.Fatal("incomplete workspace identity should be empty")
	}
}
