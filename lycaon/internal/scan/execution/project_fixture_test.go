package execution_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
)

func testProjectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "create source directory", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		testutil.FailErr(t, "write source fixture", os.WriteFile(filepath.Join(dir, "src", name), []byte("package fixture\n"), 0o644))
	}
	canonical, err := scan.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	return canonical
}
