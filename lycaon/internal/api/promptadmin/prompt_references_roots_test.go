package promptadmin

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
)

// A reference into an editor-only root reaches the model as an absolute path,
// since agents cannot address that root; references into admitted roots keep
// their root-qualified spelling.
func TestPromptReferencesNameEditorOnlyRootsByAbsolutePath(t *testing.T) {
	facts := rootfacts.Install(t)
	registry := project.NewMemoryRegistry()
	primary := true
	p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{
		{Path: facts.Work, IsPrimary: &primary}, {Path: facts.KeyMaterial},
	}})
	testutil.FailErr(t, "create project", err)
	refs := &References{Projects: registry}
	ids := map[string]string{}
	for _, root := range p.SourceRoots() {
		ids[root.Path] = root.ID
	}

	rel, target, err := refs.resolveReferencePath(t.Context(), p.ID, ids[facts.Work], "notes.txt")
	testutil.FailErr(t, "resolve admitted reference", err)
	if rel != "notes.txt" || target.RootID != ids[facts.Work] || target.Path != "notes.txt" {
		t.Fatalf("admitted reference = %q %+v", rel, target)
	}

	rel, target, err = refs.resolveReferencePath(t.Context(), p.ID, ids[facts.KeyMaterial], "config")
	testutil.FailErr(t, "resolve editor-only reference", err)
	if want := filepath.Join(facts.KeyMaterial, "config"); rel != want {
		t.Fatalf("editor-only reference = %q, want absolute %q", rel, want)
	}
	if target.RootID != ids[facts.KeyMaterial] || target.Path != "config" {
		t.Fatalf("editor-only navigation target = %+v", target)
	}
}
