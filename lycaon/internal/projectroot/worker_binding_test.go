package projectroot

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerTaskBindsRootMultiRootUnion(t *testing.T) {
	roots := []RootRef{
		{ID: "a", Path: "/a", Label: "a", IsPrimary: true},
		{ID: "b", Path: "/b", Label: "b"},
	}
	task := api.WorkerTask{WorkspaceRootID: "a", WorkspacePath: "/a"}
	if !WorkerTaskBindsRoot(task, roots[1], roots) {
		t.Fatal("multi-root worker must bind every project root")
	}
}

func TestWorkerTaskBindsRootSingleRoot(t *testing.T) {
	roots := []RootRef{{ID: "a", Path: "/a", IsPrimary: true}}
	task := api.WorkerTask{WorkspaceRootID: "a", WorkspacePath: "/a"}
	if !WorkerTaskBindsRoot(task, roots[0], roots) {
		t.Fatal("expected bind on matching root")
	}
	other := RootRef{ID: "z", Path: "/z"}
	if WorkerTaskBindsRoot(task, other, roots) {
		t.Fatal("single-root worker must not bind unrelated root")
	}
}
