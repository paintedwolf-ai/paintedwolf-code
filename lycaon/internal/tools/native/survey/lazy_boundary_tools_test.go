package survey

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

type boundaryToolScopes struct{}

func (boundaryToolScopes) Catalog(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{BoundaryDirectories: []string{"node_modules"}, NestedCheckouts: true}})
}

func TestToolsReadExplicitPathsUnderLazyBoundary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "node_modules/pkg/target.go", "package dependency\n\nfunc BoundaryTarget() {}\n")
	catalog := sourcecatalog.New()
	catalog.SetScopes(boundaryToolScopes{})
	defer func() { testutil.FailErr(t, "drain tool catalog", catalog.Drain(context.Background())) }()
	tc := nativefixture.Context(root)
	_, _, err := catalog.ObserveWithin(t.Context(), tc.ProjectID, []sourcecatalog.Root{{ID: tc.Roots[0].ID, Path: root}}, 30*time.Second)
	testutil.FailErr(t, "warm parent before explicit boundary requests", err)
	boundary := nativefixture.Boundary(t)
	summarizeTool := testSummarizeTool(t, root)
	summarizeTool.Catalog = catalog
	for _, item := range []struct {
		name string
		run  func(context.Context, map[string]any, tools.ToolContext) (string, error)
		args map[string]any
		want string
	}{
		{"read", (&ReadTool{Boundary: boundary}).Run, map[string]any{"path": "node_modules/pkg/target.go"}, "BoundaryTarget"},
		{"list_dir", (&ListDirTool{Boundary: boundary, Catalog: catalog}).Run, map[string]any{"path": "node_modules/pkg", "max_depth": 1}, "target.go"},
		{"grep from root", (&GrepTool{Boundary: boundary, Catalog: catalog}).Run, map[string]any{"path": ".", "pattern": "BoundaryTarget"}, "target.go"},
		{"find from root", (&FindTool{Boundary: boundary, Catalog: catalog}).Run, map[string]any{"path": ".", "name_glob": "*.go"}, "target.go"},
		{"grep", (&GrepTool{Boundary: boundary, Catalog: catalog}).Run, map[string]any{"path": "node_modules/pkg", "pattern": "BoundaryTarget"}, "target.go"},
		{"find", (&FindTool{Boundary: boundary, Catalog: catalog}).Run, map[string]any{"path": "node_modules/pkg", "name_glob": "*.go"}, "target.go"},
		{"summarize", summarizeTool.Run, map[string]any{"path": "node_modules/pkg", "task": "explain BoundaryTarget"}, "target.go"},
	} {
		t.Run(item.name, func(t *testing.T) {
			out, err := item.run(t.Context(), item.args, tc)
			testutil.FailErr(t, "explicit boundary "+item.name, err)
			if !strings.Contains(out, item.want) {
				t.Fatalf("explicit boundary %s missed %s: %s", item.name, item.want, out)
			}
		})
	}
	writeFile(t, root, "node_modules/pkg/target.go", "package dependency\n\nfunc UpdatedBoundaryTarget() {}\n")
	writeFile(t, root, "node_modules/pkg/added.go", "package dependency\n\nfunc AddedBoundaryTarget() {}\n")
	broad, err := (&GrepTool{Boundary: boundary, Catalog: catalog}).Run(t.Context(), map[string]any{"path": ".", "pattern": "AddedBoundaryTarget"}, tc)
	testutil.FailErr(t, "grep new unwatched body", err)
	if !strings.Contains(broad, "added.go") {
		t.Fatalf("grep omitted newly created dependency: %s", broad)
	}
	out, err := (&ReadTool{Boundary: boundary}).Run(t.Context(), map[string]any{"path": "node_modules/pkg/target.go"}, tc)
	testutil.FailErr(t, "read changed unwatched body", err)
	if !strings.Contains(out, "UpdatedBoundaryTarget") {
		t.Fatalf("read served stale boundary body: %s", out)
	}
}
