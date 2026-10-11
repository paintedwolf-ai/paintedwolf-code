package sourcescope

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBoundaryClassificationNeverChangesAdmission(t *testing.T) {
	root := t.TempDir()
	cfg, err := DefaultConfig()
	testutil.FailErr(t, "load boundary catalog", err)
	scope := New(root, Options{Plane: cfg.Catalog})
	for _, rel := range []string{"node_modules", "target", "tmp", "nested/.venv"} {
		if scope.BoundaryDir(rel) != BoundaryCurated {
			t.Errorf("missing curated boundary %s", rel)
		}
		if !scope.AdmitPath(rel+"/readable.txt", false) {
			t.Errorf("boundary rejected readable path %s", rel)
		}
	}
	for _, rel := range []string{"src", "vendor", "third_party", "deps", ".", "..", "../node_modules"} {
		if scope.BoundaryDir(rel) != "" {
			t.Errorf("ordinary directory classified lazy %s", rel)
		}
	}
	testutil.FailErr(t, "create checkout", os.Mkdir(filepath.Join(root, "checkout"), 0700))
	testutil.FailErr(t, "create checkout marker", os.WriteFile(filepath.Join(root, "checkout", ".git"), []byte("gitdir: ../elsewhere"), 0600))
	if scope.BoundaryDir("checkout") != BoundaryNestedCheckout || scope.BoundaryPath("checkout/src/readable.go", false) != BoundaryNestedCheckout {
		t.Fatal("nested checkout was not lazy")
	}
	if !scope.AdmitPath("checkout/src/readable.go", false) {
		t.Fatal("nested checkout became inaccessible")
	}
	testutil.FailErr(t, "remove checkout marker", os.Remove(filepath.Join(root, "checkout", ".git")))
	if scope.BoundaryDir("checkout") != "" {
		t.Fatal("removed checkout marker left stale classification")
	}
}
