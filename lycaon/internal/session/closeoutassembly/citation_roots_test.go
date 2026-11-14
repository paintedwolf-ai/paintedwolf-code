package closeoutassembly

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

type citationRootsStub struct{ refs []projectroot.RootRef }

func (s citationRootsStub) Roots(context.Context, *api.Session) ([]projectroot.RootRef, error) {
	return s.refs, nil
}

func TestCitationRootsResolveSessionFolderByRootID(t *testing.T) {
	primary := projectroot.RootRef{ID: "primary", Label: "app", Path: "/abs/app", IsPrimary: true}
	draft := projectroot.RootRef{ID: "draft", Label: "Draft", Path: "/abs/draft"}
	s := New(nil, citationRootsStub{[]projectroot.RootRef{primary, draft}}, nil, nil)
	ctx := context.Background()

	// A worker session's WorkspacePath is a branch directory, not a root path:
	// the session folder must still resolve to the active root by ID.
	sess := &api.Session{WorkspaceRootID: "draft", WorkspacePath: "/workers/branch-dir"}
	roots, err := s.CitationRoots(ctx, sess)
	if err != nil {
		t.Fatalf("citation roots: %v", err)
	}
	if roots.ProjectDir != "/abs/draft" {
		t.Fatalf("ProjectDir=%q want active root path", roots.ProjectDir)
	}
	if roots.ActiveRootID != "draft" {
		t.Fatalf("ActiveRootID=%q want draft", roots.ActiveRootID)
	}
	if len(roots.Roots) != 2 || roots.Roots[0].ID != "primary" {
		t.Fatalf("Roots=%+v", roots.Roots)
	}

	roots, err = s.CitationRoots(ctx, &api.Session{WorkspacePath: "/workers/branch-dir"})
	if err != nil {
		t.Fatalf("citation roots: %v", err)
	}
	if roots.ProjectDir != "/abs/app" || roots.ActiveRootID != "primary" {
		t.Fatalf("empty root id: %+v", roots)
	}

	// An unknown root ID falls back to the primary root.
	roots, err = s.CitationRoots(ctx, &api.Session{WorkspaceRootID: "gone", WorkspacePath: "/workers/branch-dir"})
	if err != nil {
		t.Fatalf("citation roots: %v", err)
	}
	if roots.ProjectDir != "/abs/app" || roots.ActiveRootID != "primary" {
		t.Fatalf("unknown root id: %+v", roots)
	}
}

func TestCitationRootsKeepsSessionFolderWithoutRoots(t *testing.T) {
	s := New(nil, citationRootsStub{nil}, nil, nil)
	roots, err := s.CitationRoots(context.Background(), &api.Session{
		WorkspaceRootID: " draft ", WorkspacePath: "/workers/branch-dir",
	})
	if err != nil {
		t.Fatalf("citation roots: %v", err)
	}
	if roots.ProjectDir != "/workers/branch-dir" {
		t.Fatalf("ProjectDir=%q", roots.ProjectDir)
	}
	if roots.ActiveRootID != "draft" {
		t.Fatalf("ActiveRootID=%q", roots.ActiveRootID)
	}
	if roots.Roots != nil {
		t.Fatalf("Roots=%+v", roots.Roots)
	}
}
