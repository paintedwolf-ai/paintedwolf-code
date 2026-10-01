package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeScanSource(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(path, []byte(content), 0o644))
}
