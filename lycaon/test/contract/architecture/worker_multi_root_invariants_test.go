package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// I11 — worker multi-root + no-folder coherence (parallel to I10).

func TestWorkerMultiRootDiscoveryUnionFind(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	tctx := fix.tctx("worker-2")
	// ToolContext.Agent carries the resolved sandbox profile, not the agent role.
	tctx.Identity.Agent = "explore_readonly"
	out, err := reg.Run(ctx, "find", map[string]any{
		"path":      ".",
		"name_glob": "*-sentinel.txt",
	}, tctx)
	contractcheck.FailErr(t, "find union worker", err)
	if !strings.Contains(out, "primary-sentinel.txt") || !strings.Contains(out, "@lycaon-den") {
		t.Fatalf("find union output = %q", out)
	}
}

func TestWorkerZeroRootsStructuredReject(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	ctx := context.Background()
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "explore_readonly",
			SessionID:   "w0",
			WorkerJobID: "job"},
	}
	for _, name := range []string{"read", "find", "grep", "list_dir", "command"} {
		t.Run(name, func(t *testing.T) {
			args := multiRootExerciseArgs(name, "x.go", true)
			_, err := reg.Run(ctx, name, args, tctx)
			if err == nil {
				t.Fatal("expected error at 0 roots")
			}
			if code := toolRejectCode(err); code != "PROJECT_HAS_NO_ROOTS" {
				t.Fatalf("code = %q want PROJECT_HAS_NO_ROOTS", code)
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
