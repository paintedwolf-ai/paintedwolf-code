package contract

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowBoundaryNoDisplayProseInGo(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "internal", "workflow", "boundary.go")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	if strings.Contains(string(data), "── ") {
		t.Fatal("internal/workflow/boundary.go must not format boundary display prose — Den supplies labels")
	}
	if strings.Contains(string(data), "boundaryContent") {
		t.Fatal("boundaryContent helper removed — structured WorkflowBoundaryMeta only")
	}
}

func TestAssemblyFiltersPromptHistory(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	checks := []struct {
		rel  string
		want string
	}{
		{"internal/coordinator/assembly/engine.go", "api.FilterPromptHistory"},
		{"internal/session/promptassembly/assembly.go", "api.FilterPromptHistory"},
		{"internal/session/compaction.go", "api.FilterPromptHistory"},
		{"internal/llm/compaction/context_message.go", "RehydrateTranscriptProjectionFields"},
	}
	for _, c := range checks {
		path := filepath.Join(root, c.rel)
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read file", err)
		if !strings.Contains(string(data), c.want) {
			t.Fatalf("%s must call %s (model-history projection SSOT)", c.rel, c.want)
		}
	}
}

func TestNewBoundaryMessageUsesSystemRole(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "internal", "workflow", "boundary.go")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	src := string(data)
	if !strings.Contains(src, "MessageRoleSystem") {
		t.Fatal("newBoundaryMessage must set Role: api.MessageRoleSystem")
	}
	if strings.Contains(src, "Role:             api.MessageRoleAssistant") ||
		strings.Contains(src, "Role: api.MessageRoleAssistant") {
		t.Fatal("newBoundaryMessage must not use MessageRoleAssistant")
	}
}

func TestSchemaAllowsSystemMessageRole(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "internal", "db", "schema.sql")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "'system'") {
		t.Fatal("messages.role CHECK must include 'system' for workflow_boundary rows")
	}
}
