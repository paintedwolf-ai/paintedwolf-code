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
