package enginebundle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRootUsesTheNearestCheckout(t *testing.T) {
	checkout := t.TempDir()
	nested := filepath.Join(checkout, "nested")
	pkg := filepath.Join(nested, "lycaon", "internal")
	testutil.FailErr(t, "create nested package", os.MkdirAll(pkg, 0o755))
	for _, root := range []string{checkout, nested} {
		testutil.FailErr(t, "write task file", os.WriteFile(filepath.Join(root, "Taskfile.yml"), nil, 0o600))
	}
	t.Chdir(pkg)
	if got, want := Root(), filepath.Join(nested, "lycaon-den", "src-tauri", "engine-root"); got != want {
		t.Fatalf("staging root = %q, want %q", got, want)
	}
}
