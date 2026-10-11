package policyindex

import (
	"testing"

	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAgentsMDMessageRetainsActiveRootAndRenderedPaths(t *testing.T) {
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}, {Path: t.TempDir()}}})
	testutil.FailErr(t, "project roots", err)
	roots := scope.New(store.NewMemory())
	roots.SetProjects(registry)
	m := New(nil, roots)
	sess := &api.Session{ProjectID: p.ID, WorkspaceRootID: p.Roots[1].ID}
	msg := m.message(t.Context(), sess, governance.AgentsMDInject{Content: "Policy from `nested/AGENTS.md`", Paths: []string{"nested/AGENTS.md", "omitted/AGENTS.md"}})
	if msg.SourceContext == nil || len(msg.SourceContext.Locations) != 1 {
		t.Fatalf("injected source context = %+v", msg.SourceContext)
	}
	target := msg.SourceContext.Locations[0]
	if target.RootID != sess.WorkspaceRootID || target.Path != "nested/AGENTS.md" || msg.Authority != api.ContentAuthorityDeveloper {
		t.Fatalf("policy identity = %+v", msg)
	}
}
