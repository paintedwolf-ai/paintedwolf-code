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
		SessionID:   "sess-inject-test",
		ProjectDir:  t.TempDir(),
		Charter:     testWorkerCharter("Add auth middleware"),
		AgentType:   "implementer",
		WorkerJobID: "job-login-7",
		Scope:       api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"auth.go"}},
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment", err)
	for _, want := range []string{
		"record_finding",
		"read_findings",
		"Sibling edits never appear here live.",
	} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Fatalf("worker assignment missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "pack_board(finding") {
		t.Fatalf("worker assignment still teaches pack_board findings: %s", out)
	}
	mounted, err := RenderWorkerTaskAssignment(context.Background(), testInjectRenderer(t), WorkerTaskAssignmentInput{
		SessionID:         "sess-inject-test",
		ProjectDir:        t.TempDir(),
		Charter:           testWorkerCharter("Add auth middleware"),
		WorkerName:        "auth-impl",
		RoundInstructions: "Also wire middleware tests.",
		ViewKind:          "mounted",
		AgentType:         "implementer",
		WorkerJobID:       "job-login-8",
		Scope:             api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"auth.go"}},
	})
	testutil.FailErr(t, "RenderWorkerTaskAssignment mounted", err)
	if strings.Contains(mounted, "Sibling edits never appear here live.") {
		t.Fatalf("mounted view claimed sibling edits never appear live: %s", mounted)
	}
	if !strings.Contains(mounted, "auth-impl") || !strings.Contains(mounted, "Also wire middleware tests.") {
		t.Fatalf("mounted assignment missing worker name or round instructions: %s", mounted)
	}
}
