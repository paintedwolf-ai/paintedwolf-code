package projectcontrol

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
	"reflect"
	"testing"
)

type detachSessions struct {
	Sessions
	sessions []*api.Session
	reassign []string
}

func (s *detachSessions) List(context.Context) ([]*api.Session, error) { return s.sessions, nil }
func (s *detachSessions) ReassignSessionsWorkspaceRoot(_ context.Context, p, r, replacement string) error {
	s.reassign = []string{p, r, replacement}
	return nil
}
func (s *detachSessions) UpdateSession(_ context.Context, id string, fn func(*api.Session)) error {
	for _, sess := range s.sessions {
		if sess != nil && sess.ID == id {
			fn(sess)
		}
	}
	return nil
}

type detachActions struct {
	stopped, idle, kicks []string
	roots                []string
}

func (a *detachActions) Abort(_ context.Context, id, _ string) error {
	a.stopped = append(a.stopped, id)
	return nil
}
func (a *detachActions) PublishIdle(_ context.Context, id string, _ api.SessionIdleDisposition) {
	a.idle = append(a.idle, id)
}
func (a *detachActions) Emit(_ context.Context, id string, _ anchor.ID, _ anchor.Envelope) {
	a.kicks = append(a.kicks, id)
}
func (a *detachActions) AbortWorkersForRoot(_ context.Context, _, root string, _ []projectroot.RootRef, _ string) error {
	a.roots = append(a.roots, root)
	return nil
}

type detachProjects struct{ p *project.Project }

func (p detachProjects) Get(context.Context, string) (*project.Project, error) { return p.p, nil }

func TestProjectDeleteStopsDependenciesAndRetargetsDetachedSessions(t *testing.T) {
	sessions := &detachSessions{sessions: []*api.Session{{ID: "busy", ProjectID: "project", WorkspaceRootID: "one", WorkspacePath: "cached", Status: api.SessionStatusBusy}, {ID: "idle", ProjectID: "project", Status: api.SessionStatusIdle}, {ID: "foreign", ProjectID: "foreign", WorkspacePath: "untouched", Status: api.SessionStatusBusy}, nil}}
	actions := &detachActions{}
	s := New(sessions, actions, actions, nil, nil, nil, nil)
	s.SetProjects(detachProjects{&project.Project{ID: "project", Roots: []project.Root{{ID: "one", Path: t.TempDir(), IsPrimary: true}, {ID: "two", Path: t.TempDir()}}}})
	s.SetWorkerAbort(actions)
	s.SetAnchors(actions)
	deps, err := s.ProjectDependents(t.Context(), "project")
	if err != nil || len(deps.Sessions) != 1 || deps.Sessions[0].SessionID != "busy" {
		t.Fatalf("dependents=%+v err=%v", deps, err)
	}
	if !deps.HasAny() || len(deps.Details()["sessions"].([]map[string]any)) != 1 {
		t.Fatal("dependency details lost busy session")
	}
	if err = s.ForceCancelForProjectDelete(t.Context(), "project", deps); err != nil {
		t.Fatalf("s.ForceCancelForProjectDelete failed: %v", err)
	}
	if !reflect.DeepEqual(actions.stopped, []string{"busy"}) || !reflect.DeepEqual(actions.idle, actions.stopped) || !reflect.DeepEqual(actions.roots, []string{"one", "two"}) || !reflect.DeepEqual(actions.kicks, []string{"busy", "idle"}) {
		t.Fatalf("actions=%+v", actions)
	}
	s.ReassignSessionsAfterRootDetach(t.Context(), "project", "one", []projectroot.RootRef{{ID: "two", Path: t.TempDir(), IsPrimary: true}})
	if !reflect.DeepEqual(sessions.reassign, []string{"project", "one", "two"}) {
		t.Fatalf("retarget=%v", sessions.reassign)
	}
	s.InvalidateSessionWorkspacePaths(t.Context(), "project")
	if sessions.sessions[0].WorkspacePath != "" || sessions.sessions[2].WorkspacePath != "untouched" {
		t.Fatal("workspace invalidation crossed project")
	}
}
