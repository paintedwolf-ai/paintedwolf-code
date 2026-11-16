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
)

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
