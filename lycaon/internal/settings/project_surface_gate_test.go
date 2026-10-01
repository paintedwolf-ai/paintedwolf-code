package settings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/testutil"
)

// stubProjectLookup serves a fixed project list; Get is keyed by id.
type stubProjectLookup struct {
	projects []project.Project
	listErr  error
}

func (s stubProjectLookup) Get(_ context.Context, id string) (*project.Project, error) {
	for i := range s.projects {
		if s.projects[i].ID == id {
			p := s.projects[i]
			return &p, nil
		}
	}
	return nil, errors.New("not found")
}

func (s stubProjectLookup) List(context.Context) ([]project.Project, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]project.Project(nil), s.projects...), nil
}

func trustProjectAt(id, root string, surfaceOn bool) project.Project {
	p := project.Project{
		ID:              id,
		Roots:           []project.Root{{Path: root}},
		RootsGeneration: 1,
	}
	if !surfaceOn {
		p.TrustEnabled = map[string]bool{projectcontrib.SurfaceProjectSettings: false}
	}
	return p
}

func newPathGate(t *testing.T, projects ...project.Project) *ProjectSurfaceGate {
	t.Helper()
	store, err := NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
	testutil.FailErr(t, "NewTrustSurfacesStoreAt", err)
	return &ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceProjectSettings,
		Surfaces: store,
		Projects: stubProjectLookup{projects: projects},
	}
}

// A child project must resolve to its own trust record, not the parent's.
func TestAppliesPathNestedProjectOwnsItsPath(t *testing.T) {
	t.Parallel()
	outer := t.TempDir()
	inner := filepath.Join(outer, "untrusted-repo")

	t.Run("inner_off_under_outer_on", func(t *testing.T) {
		t.Parallel()
		gate := newPathGate(t,
			trustProjectAt("outer", outer, true),
			trustProjectAt("inner", inner, false),
		)
		if gate.AppliesPath(context.Background(), inner) {
			t.Fatal("nested project must use its own off switch")
		}
		if gate.AppliesPath(context.Background(), filepath.Join(inner, "pkg")) {
			t.Fatal("paths under the nested root must use the nested project")
		}
		if !gate.AppliesPath(context.Background(), filepath.Join(outer, "sibling")) {
			t.Fatal("paths outside the nested root must use the outer project")
		}
	})

	t.Run("registry_order_does_not_decide", func(t *testing.T) {
		t.Parallel()
		// Same tree, nested project listed first: the answer must not depend on order.
		gate := newPathGate(t,
			trustProjectAt("inner", inner, false),
			trustProjectAt("outer", outer, true),
		)
		if gate.AppliesPath(context.Background(), inner) {
			t.Fatal("nested project must use its own off switch")
		}
	})

	t.Run("inner_on_under_outer_off", func(t *testing.T) {
		t.Parallel()
		gate := newPathGate(t,
			trustProjectAt("outer", outer, false),
			trustProjectAt("inner", inner, true),
		)
		if !gate.AppliesPath(context.Background(), inner) {
			t.Fatal("nested project must use its own on switch")
		}
		if gate.AppliesPath(context.Background(), outer) {
			t.Fatal("outer project must stay off")
		}
	})
}

// TestAppliesPathClosedWhenContainmentUnresolved pins the fail-closed edges.
func TestAppliesPathClosedWhenContainmentUnresolved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	t.Run("ambiguous_same_root", func(t *testing.T) {
		t.Parallel()
		gate := newPathGate(t,
			trustProjectAt("a", dir, true),
			trustProjectAt("b", dir, true),
		)
		if gate.AppliesPath(context.Background(), dir) {
			t.Fatal("two projects claiming one root is ambiguous containment; must fail closed")
		}
	})

	t.Run("unknown_path", func(t *testing.T) {
		t.Parallel()
		gate := newPathGate(t, trustProjectAt("a", dir, true))
		if gate.AppliesPath(context.Background(), filepath.Join(t.TempDir(), "elsewhere")) {
			t.Fatal("unknown path applied project content")
		}
	})

	t.Run("sibling_prefix_is_not_a_parent", func(t *testing.T) {
		t.Parallel()
		gate := newPathGate(t, trustProjectAt("a", dir, true))
		if gate.AppliesPath(context.Background(), dir+"-other") {
			t.Fatal("a string prefix that is not a path ancestor must not match")
		}
	})

	t.Run("registry_error", func(t *testing.T) {
		t.Parallel()
		store, err := NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
		testutil.FailErr(t, "NewTrustSurfacesStoreAt", err)
		gate := &ProjectSurfaceGate{
			Surface:  projectcontrib.SurfaceProjectSettings,
			Surfaces: store,
			Projects: stubProjectLookup{listErr: errors.New("registry down")},
		}
		if gate.AppliesPath(context.Background(), dir) {
			t.Fatal("registry read failure must fail closed")
		}
	})
}

// TestFilterPathsKeepsOnlyOwnedApprovedRoots covers the scan-side entry point.
func TestFilterPathsKeepsOnlyOwnedApprovedRoots(t *testing.T) {
	t.Parallel()
	outer := t.TempDir()
	inner := filepath.Join(outer, "vendored")
	gate := newPathGate(t,
		trustProjectAt("outer", outer, true),
		trustProjectAt("inner", inner, false),
	)
	kept := gate.FilterPaths(context.Background(), []string{outer, inner})
	if len(kept) != 1 || kept[0] != outer {
		t.Fatalf("FilterPaths = %v, want only %q", kept, outer)
	}
}
