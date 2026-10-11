package composition_test

import (
	"path/filepath"
	"runtime"
	"testing"
)

func bundledWorkflowsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
}
