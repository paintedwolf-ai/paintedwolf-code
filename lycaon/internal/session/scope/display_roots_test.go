package scope

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeScopeStore struct {
	sessions map[string]*api.Session
	bindings map[string]*store.WorktreeBinding
	failGet  bool
}

func (s fakeScopeStore) Get(_ context.Context, id string) (*api.Session, error) {
	if s.failGet {
		return nil, errors.New("store unavailable")
	}
	return s.sessions[id], nil
}

func (s fakeScopeStore) GetWorktreeBinding(_ context.Context, id string) (*store.WorktreeBinding, bool, error) {
	b, ok := s.bindings[id]
	return b, ok, nil
}

type fakeProjects map[string]*project.Project

func (p fakeProjects) Get(_ context.Context, id string) (*project.Project, error) {
	return p[id], nil
}

// Recorded paths display against the roots the session works in: its
// project's roots, with a bound worktree in their place even after the
// worktree went stale, so old receipts keep rendering.
func TestDisplayRootsFollowTheSessionsWorkingRoots(t *testing.T) {
	top := t.TempDir()
	stale := filepath.Join(t.TempDir(), "removed-worktree")
	projects := fakeProjects{"p1": {ID: "p1", Roots: []project.Root{
		{ID: "r1", ProjectID: "p1", Path: top, IsPrimary: true},
		{ID: "r2", ProjectID: "p1", Path: filepath.Join(top, "service")},
	}}}
	sessions := map[string]*api.Session{
		"plain": {ID: "plain", ProjectID: "p1"},
		"bound": {ID: "bound", ProjectID: "p1"},
	}
	bindings := map[string]*store.WorktreeBinding{
		"bound": {WorktreeID: "w1", SessionID: "bound", Toplevel: top, WorktreePath: stale, Branch: "work"},
	}
	svc := New(fakeScopeStore{sessions: sessions, bindings: bindings})
	svc.SetProjects(projects)
	projectRoots := svc.ProjectRoots(t.Context(), "p1")
	if len(projectRoots) != 2 {
		t.Fatalf("fixture roots not admitted: %+v", projectRoots)
	}
	if got := svc.DisplayRoots(t.Context(), "plain"); !slices.Equal(got, projectRoots) {
		t.Errorf("unbound session displays against %+v, want project roots %+v", got, projectRoots)
	}
	got := svc.DisplayRoots(t.Context(), "bound")
	want := project.SubstituteWorktreeRoots(projectRoots, project.Binding{ID: "w1", Toplevel: top, WorktreePath: stale, Branch: "work"})
	if !slices.Equal(got, want) || slices.Equal(got, projectRoots) {
		t.Errorf("bound session displays against %+v, want its worktree roots %+v", got, want)
	}
	if _, err := svc.Roots(t.Context(), sessions["bound"]); !errors.Is(err, ErrWorktreeStale) {
		t.Fatalf("fixture worktree is not stale: %v", err)
	}
	for name, roots := range map[string][]projectroot.RootRef{
		"unknown session": svc.DisplayRoots(t.Context(), "missing"),
		"store failure":   (&Service{store: fakeScopeStore{failGet: true}, projects: projects}).DisplayRoots(t.Context(), "plain"),
		"no store":        (&Service{projects: projects}).DisplayRoots(t.Context(), "plain"),
		"no service":      (*Service)(nil).DisplayRoots(t.Context(), "plain"),
	} {
		if roots != nil {
			t.Errorf("%s displays against %+v, want none", name, roots)
		}
	}
}
