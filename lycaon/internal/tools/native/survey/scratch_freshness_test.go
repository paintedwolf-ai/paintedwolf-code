package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestScratchSurveySeesWritesAfterEmptyInventory(t *testing.T) {
	root, scratch := t.TempDir(), t.TempDir()
	tc := nativefixture.Context(root)
	tc.Host.SessionScratchDir = scratch
	catalog := sourcecatalog.New()
	_, err := catalog.Observe(t.Context(), tc.Identity.ProjectID, []sourcecatalog.Root{{ID: projectroot.VirtualScratchLabel, Path: scratch}})
	testutil.FailErr(t, "observe empty scratch", err)
	testutil.FailErr(t, "write scratch script", os.WriteFile(filepath.Join(scratch, "test-app.mjs"), []byte("console.log('needle');\n"), 0o600))
	boundary := nativefixture.Boundary(t)
	finder := &FindTool{Boundary: boundary, Catalog: catalog}
	out, err := finder.Run(t.Context(), map[string]any{"path": "@scratch", "name_glob": "*.mjs"}, tc)
	testutil.FailErr(t, "find new scratch script", err)
	if found := parseFindResponse(t, out); len(found.Results) != 1 {
		t.Errorf("find missed scratch write: %s", out)
	}
	listing := &ListDirTool{Boundary: boundary, Catalog: catalog}
	out, err = listing.Run(t.Context(), map[string]any{"path": "@scratch"}, tc)
	testutil.FailErr(t, "list new scratch script", err)
	if found := decodeListDirOut(t, out); len(found.Entries) != 1 {
		t.Errorf("list_dir missed scratch write: %s", out)
	}
	grepper := &GrepTool{Boundary: boundary, Catalog: catalog}
	out, err = grepper.Run(t.Context(), map[string]any{"path": "@scratch", "pattern": "needle"}, tc)
	testutil.FailErr(t, "search new scratch script", err)
	if found := nativefixture.GrepMatches(t, out); len(found) != 1 {
		t.Errorf("grep missed scratch write: %s", out)
	}
}

type unavailableScratchInventory struct {
	sourceInventoryCatalog
	err error
}

func (c unavailableScratchInventory) ObserveDependencyScope(context.Context, sourcecatalog.Root) (sourcecatalog.Snapshot, error) {
	return sourcecatalog.Snapshot{}, c.err
}
func TestScratchInventoryPreservesObservationError(t *testing.T) {
	expected := errors.New("scratch observation unavailable")
	root := projectroot.RootRef{ID: projectroot.VirtualScratchLabel, Path: t.TempDir()}
	_, err := sourceInventoryForScope(t.Context(), unavailableScratchInventory{err: expected}, "project", root, root.Path)
	if !errors.Is(err, expected) {
		t.Fatalf("observation error = %v, want %v", err, expected)
	}
}
func TestScratchInventoryScopesFreshNestedFiles(t *testing.T) {
	root := projectroot.RootRef{ID: projectroot.VirtualScratchLabel, Path: t.TempDir()}
	writeInventoryTree(t, root.Path, "nested/a.go", "other/b.go")
	catalog := sourcecatalog.New()
	for _, count := range []int{1, 0} {
		if count == 0 {
			testutil.FailErr(t, "remove scratch file", os.Remove(filepath.Join(root.Path, "nested/a.go")))
		}
		inventory, err := sourceInventoryForScope(t.Context(), catalog, "project", root, filepath.Join(root.Path, "nested"))
		testutil.FailErr(t, "observe scratch subdirectory", err)
		if got := inventoryPaths(t, inventory); len(got) != count {
			t.Fatalf("scoped scratch paths = %v, want %d", got, count)
		}
	}
}
