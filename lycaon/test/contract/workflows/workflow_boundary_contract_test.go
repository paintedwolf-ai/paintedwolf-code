package contract

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowBoundaryNoDisplayProseInGo(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "internal", "workflow", "runstate", "journal.go")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	if strings.Contains(string(data), "── ") {
		t.Fatal("internal/workflow/runstate/journal.go must not format boundary display prose — Den supplies labels")
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
		{"internal/session/history/compaction.go", "api.FilterPromptHistory"},
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
	msg := runstate.NewBoundaryMessage(&api.WorkflowRun{ID: "run", SessionID: "session", WorkflowID: "plan", WorkflowVersion: "1.0.0"}, "started", "research", "")
	if msg.Role != api.MessageRoleSystem || msg.Kind != api.MessageKindWorkflowBoundary || msg.WorkflowBoundary == nil {
		t.Fatalf("boundary message must use a structured system row: %+v", msg)
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
