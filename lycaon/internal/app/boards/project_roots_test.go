package boards

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResearchAndCatalogResolveAttachedRootsToTheirProject(t *testing.T) {
	database := testdbfixture.Open(t, "research-roots.db")
	registry := project.NewSQLRegistry(database)
	primary, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve primary fixture path", err)
	secondary, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve secondary fixture path", err)
	yes, no := true, false
	p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{
		{Path: secondary, IsPrimary: &no}, {Path: primary, IsPrimary: &yes},
	}})
	testutil.FailErr(t, "create two-root project", err)
	runtime := New(Dependencies{Storage: persistence.Runtime{Projects: registry}})
	paths, err := runtime.ProjectRootPaths(t.Context())
	testutil.FailErr(t, "resolve research roots", err)
	if len(paths) != 2 || paths[0] != primary || paths[1] != secondary {
		t.Fatalf("research root precedence=%v", paths)
	}
	for _, root := range p.Roots {
		dirtyPath := " " + filepath.Join(root.Path, ".") + " "
		id, err := runtime.projectIDForRoot(t.Context(), dirtyPath)
		testutil.FailErr(t, "resolve research project", err)
		identity, ok, err := runtime.repoCatalogRoot(t.Context(), dirtyPath)
		testutil.FailErr(t, "resolve catalog identity", err)
		if id != p.ID || !ok || identity.ProjectID != p.ID || identity.RootID != root.ID {
			t.Fatalf("attached root crossed identity: research=%q catalog=%+v,%v", id, identity, ok)
		}
	}
	unknown := t.TempDir()
	if id, err := runtime.projectIDForRoot(t.Context(), unknown); err != nil || id != "" {
		t.Fatalf("unknown root acquired research project:%q,%v", id, err)
	}
	if _, ok, err := runtime.repoCatalogRoot(t.Context(), unknown); err != nil || ok {
		t.Fatalf("unknown root acquired catalog identity:%v,%v", ok, err)
	}
	if count, known := runtime.repoCatalogFileCount(primary); known || count != 0 {
		t.Fatalf("unsettled inventory invented a file count:%d,%v", count, known)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.ProjectRootPaths(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled research enumeration=%v", err)
	}
	if _, err := runtime.projectIDForRoot(canceled, primary); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled project resolution=%v", err)
	}
	if _, _, err := runtime.repoCatalogRoot(canceled, primary); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled catalog resolution=%v", err)
	}
}
