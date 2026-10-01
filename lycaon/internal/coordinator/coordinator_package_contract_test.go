package coordinator_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

var deletedSessionCoordinatorFiles = []string{
	"prompt_loop.go",
	"tool_policy.go",
	"coordinator_context.go",
	"coordinator_playbook.go",
	"loop_wake.go",
	"loop_wake_policy.go",
}

func TestSessionPackageCoordinatorFilesRemoved(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	sessionDir := filepath.Join(filepath.Dir(file), "..", "session")
	for _, name := range deletedSessionCoordinatorFiles {
		path := filepath.Join(sessionDir, name)
		if _, err := os.Stat(path); err == nil {
			t.Errorf("session file %q should be removed (moved to coordinator)", name)
		}
	}
}

func TestAPICannotImportCoordinator(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	apiDir := filepath.Join(filepath.Dir(file), "..", "api")
	entries, err := os.ReadDir(apiDir)
	testutil.FailErr(t, "read directory entries", err)
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(apiDir, ent.Name()))
		testutil.FailErr(t, "read file", err)
		if strings.Contains(string(data), `"github.com/lycaon/lycaon/internal/coordinator"`) {
			t.Errorf("api package must not import coordinator: %s", ent.Name())
		}
	}
}

func TestBuildCompletionMessagesSingleImplementation(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	coordDir := filepath.Dir(file)
	sessionDir := filepath.Join(coordDir, "..", "session")
	sessionCount := countSubstringInGoFiles(t, sessionDir, "func (m *Manager) buildCompletionMessages")
	if sessionCount > 1 {
		t.Fatalf("session has %d buildCompletionMessages implementations", sessionCount)
	}
	data, err := os.ReadFile(filepath.Join(coordDir, "assembly/engine.go"))
	testutil.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "BuildCompletionMessages") {
		t.Fatal("coordinator/assembly/engine.go must define BuildCompletionMessages")
	}
}

func countSubstringInGoFiles(t *testing.T, dir, needle string) int {
	t.Helper()
	n := 0
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read directory entries", err)
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		testutil.FailErr(t, "read file", err)
		if strings.Contains(string(data), needle) {
			n++
		}
	}
	return n
}
