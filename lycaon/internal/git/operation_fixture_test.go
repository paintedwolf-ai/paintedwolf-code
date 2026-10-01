//go:build integration || stress

package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func operationFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gittest.Init(t, dir)
	gittest.Run(t, dir, "config", "user.name", "Test")
	gittest.Run(t, dir, "config", "user.email", "test@example.com")
	operationWrite(t, dir, "shared.txt", "base\n")
	operationWrite(t, dir, "unrelated.txt", "original\n")
	gittest.Run(t, dir, "add", "--", "shared.txt", "unrelated.txt")
	gittest.Run(t, dir, "commit", "-m", "Initial")
	gittest.Run(t, dir, "branch", "-M", "main")
	return dir
}
func operationWrite(t *testing.T, dir, path, body string) {
	t.Helper()
	testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755))
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644))
}
func operateOK(t *testing.T, dir string, req OperationRequest) OperationResult {
	t.Helper()
	result, err := NewManager().Operate(t.Context(), dir, req)
	testutil.FailErr(t, "operate", err)
	if result.Status != "completed" && result.Status != "no_op" {
		t.Fatalf("operation failed: %+v", result)
	}
	return result
}
