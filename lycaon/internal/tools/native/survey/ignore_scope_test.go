package survey

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeIgnoredTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755))
	testutil.FailErr(t, "write .gitignore", os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\nsecret.env\n"), 0o644))
	testutil.FailErr(t, "write dep", os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("needle\n"), 0o644))
	testutil.FailErr(t, "write ignored", os.WriteFile(filepath.Join(dir, "secret.env"), []byte("needle\n"), 0o644))
	testutil.FailErr(t, "write src", os.WriteFile(filepath.Join(dir, "main.go"), []byte("// needle\n"), 0o644))
	return dir
}
