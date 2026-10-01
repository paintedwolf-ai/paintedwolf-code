package assembly

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerWorkspaceBoard struct {
	paths []string
	roots [][]projectroot.RootRef
	files int
}

func (b *workerWorkspaceBoard) BuildBoardSnapshot(_ context.Context, _, path, _ string, _ api.BoardDetailLevel, roots []projectroot.RootRef) (*api.BoardSnapshot, error) {
	b.paths = append(b.paths, path)
	b.roots = append(b.roots, roots)
	return &api.BoardSnapshot{Repo: api.RepoBrief{FileCount: b.files, GeneratedAt: time.Now().UTC()}}, nil
}

func TestWorkerBoardUsesCurrentBranchInventory(t *testing.T) {
	builder := &workerWorkspaceBoard{files: 2}
	engine := NewBoardEngine(builder, nil, nil)
	engine.SetInjectRenderer(promptstest.InjectRenderer(t))
	branch := t.TempDir()
	engine.SetWorkerRoots(func(context.Context, *api.Session) ([]projectroot.RootRef, error) {
		return []projectroot.RootRef{{ID: "root", Path: branch, Label: "workspace", IsPrimary: true}}, nil
	})
	sess := &api.Session{ID: "worker", ParentSessionID: "parent", ProjectID: "project", WorkspaceRootID: "root", WorkspacePath: t.TempDir()}
	first, ok := engine.WorkerBoard(t.Context(), sess)
	if !ok || !strings.Contains(first, "Repo: 2 files") || !strings.Contains(first, branch) {
		t.Fatalf("initial board = %q", first)
	}
	builder.files = 3
	second, ok := engine.WorkerBoard(t.Context(), sess)
	if !ok || !strings.Contains(second, "Repo: 3 files") || strings.Contains(second, "Repo: 2 files") {
		t.Fatalf("board retained inventory from before the write: %q", second)
	}
	if len(builder.paths) != 2 || builder.paths[1] != branch || len(builder.roots[1]) != 1 || builder.roots[1][0].Path != branch {
		t.Fatalf("board used primary roots: paths=%v roots=%+v", builder.paths, builder.roots)
	}
}
