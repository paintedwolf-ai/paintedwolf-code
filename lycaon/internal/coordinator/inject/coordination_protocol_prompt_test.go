package inject

import (
	"context"
	"github.com/lycaon/lycaon/internal/configlayout"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// testPromptEngine loads bundled prompts from the module root.
func testPromptEngine(t *testing.T) *prompts.FileTemplateEngine {
	t.Helper()
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
}

func TestCoordinationProtocolInCoordinatorCore(t *testing.T) {
	engine := testPromptEngine(t)
	vars := map[string]any{
		"execution_mode":         "orchestrate",
		"has_file_tools":         true,
		"can_orient":             true,
		"can_spawn_web_research": true,
	}
	for k, v := range prompts.CoordinatorPolicyTemplateVars([]string{"read", "grep", "find", "task", "update_progress"}, nil) {
		vars[k] = v
	}
	rendered, err := engine.Render(context.Background(), "agents/coordinator-core.md", vars)
	testutil.FailErr(t, "Render coordinator-core", err)
	for _, want := range []string{
		"update_progress",
		"record_finding(summary, ref, body?)",
		"host records worker lifecycle in the worklog",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("coordinator core missing %q", want)
		}
	}
}

func TestCoordinationProtocolInWorkerTaskAssignment(t *testing.T) {
	out, err := RenderWorkerTaskAssignment(context.Background(), testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID:    "sess-inject-test",
		ProjectDir:   t.TempDir(),
		Charter:      testWorkerCharter("Add auth middleware"),
		AgentType:    "implementer",
		WorkerJobID:  "job-login-7",
		Scope:        api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"auth.go"}},
		MaxToolLoops: 12,
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment", err)
	for _, want := range []string{
		"record_finding",
		"pack_board",
	} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Fatalf("worker assignment missing %q in %q", want, out)
		}
	}
}
