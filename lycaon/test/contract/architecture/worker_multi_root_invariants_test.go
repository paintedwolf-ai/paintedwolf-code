package contract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// I11 — worker multi-root + no-folder coherence (parallel to I10).

func TestWorkerMultiRootDiscoveryUnionFind(t *testing.T) {
	fix := newMultiRootFixture(t)
	tc := fix.tctx("worker-2")
	tc.Identity.Agent = "explore_readonly"
	out := assertRootProgramSuccess(t, rootProgram(t, "find . @lycaon-den -name '*-sentinel.txt'", tc))
	if !strings.Contains(out, "primary-sentinel.txt") || !strings.Contains(out, filepath.Join(fix.secondaryDir, "secondary-sentinel.txt")) {
		t.Fatalf("worker root discovery=%q", out)
	}
}

func TestWorkerZeroRootsStructuredReject(t *testing.T) {
	f := toolfixture.NewProgramFixture(t)
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "explore_readonly", WorkerJobID: "job"}}
	for _, source := range []string{"cat x.go", "find .", "rg pattern .", "ls .", "pwd"} {
		t.Run(source, func(t *testing.T) {
			out, err := f.Invoke(t, source, tc)
			code := toolRejectCode(err)
			if out.Refusal != nil {
				code = out.Refusal.Code
			}
			if code != "PROJECT_HAS_NO_ROOTS" {
				t.Fatalf("no-root worker program: code=%s outcome=%+v error=%v", code, out, err)
			}
		})
	}
}

func TestWebResearcherPromptNoFolderCoherent(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	rendered, err := prompts.RenderPersona(context.Background(), engine, "web-researcher", map[string]any{
		"root_count":             0,
		"has_file_tools":         false,
		"can_orient":             false,
		"can_spawn_implementer":  false,
		"can_spawn_web_research": true,
		"max_tool_loops":         30,
	})
	contractcheck.FailErr(t, "RenderPersona web-researcher", err)
	if !strings.Contains(rendered, "No folder is attached") {
		t.Fatalf("web-researcher missing no-folder workspace framing:\n%s", rendered)
	}
	if strings.Contains(rendered, "Workspace:") {
		t.Fatal("web-researcher must not advertise a workspace path at 0 roots")
	}
}
