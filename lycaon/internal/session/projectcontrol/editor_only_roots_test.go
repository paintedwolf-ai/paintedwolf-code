package projectcontrol

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

type reassignSessions struct {
	Sessions
	sessions []*api.Session
	moves    [][3]string
}

func (s *reassignSessions) List(context.Context) ([]*api.Session, error) { return s.sessions, nil }
func (s *reassignSessions) ReassignSessionsWorkspaceRoot(_ context.Context, projectID, from, to string) error {
	s.moves = append(s.moves, [3]string{projectID, from, to})
	return nil
}

type reassignProjects map[string]*project.Project

func (p reassignProjects) Get(_ context.Context, id string) (*project.Project, error) {
	return p[id], nil
}

func TestChatsLeaveEditorOnlyRootsForTheAgentsPrimary(t *testing.T) {
	facts := rootfacts.Install(t)
	mixed := &project.Project{ID: "mixed", Roots: []project.Root{
		{ID: "home", Path: facts.ContainsState, IsPrimary: true},
		{ID: "work", Path: facts.Work},
		{ID: "keys", Path: facts.KeyMaterial},
	}}
	editorOnly := &project.Project{ID: "editor-only", Roots: []project.Root{{ID: "store", Path: facts.CredentialStore, IsPrimary: true}}}
	sessions := &reassignSessions{sessions: []*api.Session{
		{ID: "a", ProjectID: "mixed", WorkspaceRootID: "home"},
		{ID: "b", ProjectID: "mixed", WorkspaceRootID: "home"},
		{ID: "c", ProjectID: "mixed", WorkspaceRootID: "keys"},
		{ID: "d", ProjectID: "mixed", WorkspaceRootID: "work"},
		{ID: "e", ProjectID: "mixed", WorkspaceRootID: "detached"},
		{ID: "f", ProjectID: "editor-only", WorkspaceRootID: "store"},
		{ID: "g", ProjectID: "mixed"},
		nil,
	}}
	s := New(sessions, nil, nil, nil, nil, nil, nil)
	s.SetProjects(reassignProjects{"mixed": mixed, "editor-only": editorOnly})

	testutil.FailErr(t, "reassign project sessions", s.ReassignProjectSessionsOffEditorOnlyRoots(t.Context(), "mixed"))
	if want := [][3]string{{"mixed", "home", "work"}, {"mixed", "keys", "work"}}; !reflect.DeepEqual(sessions.moves, want) {
		t.Fatalf("project moves = %v, want %v", sessions.moves, want)
	}

	sessions.moves = nil
	testutil.FailErr(t, "reassign sessions", s.ReassignSessionsOffEditorOnlyRoots(t.Context()))
	if want := [][3]string{{"mixed", "home", "work"}, {"mixed", "keys", "work"}, {"editor-only", "store", ""}}; !reflect.DeepEqual(sessions.moves, want) {
		t.Fatalf("startup moves = %v, want %v", sessions.moves, want)
	}
}
