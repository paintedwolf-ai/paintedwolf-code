package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var implementInvestigateSurfaceTools = []string{
	"read", "grep", "summarize", "skills_read", "find", "list_dir",
	"update_progress", "ask_user", "surface_note", "request_tools",
	"recall",
}

func TestCoordinatorToolSurfaceSecretsRemainRequestableWithOutboundExecution(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: tools.SurfaceImplementInvestigate}, 1)
	contractcheck.FailErr(t, "CompileToolPlan", err)
	deferred := plan.DeferredNames()
	for _, name := range []string{"http_request", "process_list", "process_signal", "secret_generate", "secret_list", "secret_revoke"} {
		if !contractcheck.ContainsString(deferred, name) {
			t.Fatalf("implement_investigate missing deferred outbound capability %q", name)
		}
		if !contractcheck.ContainsString(surfaces["implement_overlay_promote"], name) {
			t.Fatalf("implement_overlay_promote missing sticky outbound capability %q", name)
		}
	}
}

func TestCoordinatorToolSurface_managedSecretLifecycleFollowsMutationCapability(t *testing.T) {
	t.Parallel()
	for surfaceID := range loadImplementSurfaces(t) {
		plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
		contractcheck.FailErr(t, "compile tool plan for "+surfaceID, err)
		available := plan.AddressableNames()
		if !prompts.ToolNamesMutationCapable(available) {
			continue
		}
		if !contractcheck.ContainsString(available, "skills_read") {
			t.Errorf("mutation-capable coordinator surface %q cannot read the managed-secret procedure", surfaceID)
		}
		for _, name := range managedSecretLifecycleTools {
			if !contractcheck.ContainsString(available, name) {
				t.Errorf("mutation-capable coordinator surface %q is missing %q", surfaceID, name)
			}
		}
	}
}

var implementInvestigateDeferredTools = []string{
	"survey_repo", "jq", "stat", "wc",
	"write", "edit", "replace_lines", "restore_version", "code_rewrite", "jq_edit", "diff", "source_history", "delete", "copy", "move", "mkdir", "chmod", "chown", "extract_archive",
	"command", "command_output", "command_stop", "process_list", "process_signal", "verify", "wait",
	"terminal_open", "terminal_send", "terminal_read", "terminal_snapshot", "terminal_close",
	"git_status", "git_diff", "git_log", "git_show", "git_blame", "git_ref", "git_branches", "git_compare", "git_commit", "git_restore", "git_checkout", "git_merge", "git_stash", "git_stash_list",
	"render_view", "view_image", "view_video", "capture_page", "measure_page", "page_open", "page_act", "page_snapshot", "page_close",
	"secret_generate", "secret_list", "secret_revoke", "http_request",
	"web_search", "fetch_url",
	"scan_pack", "scan_query", "scan_summary", "scan_list", "scan_compare",
	"task", "pack_board", "worker_cancel", "answer_decision", "extend_worker_budget", "decline_worker_budget",
}

func TestCoordinatorToolSurface_implementInvestigateMatchesSSOT(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	got := append([]string(nil), surfaces[tools.SurfaceImplementInvestigate]...)
	sortStrings(got)
	want := append([]string(nil), implementInvestigateSurfaceTools...)
	sortStrings(want)
	if !sliceEqual(got, want) {
		t.Fatalf("implement_investigate tools = %v want %v", got, want)
	}
}

func TestCoordinatorToolSurface_implementInvestigateDeferred(t *testing.T) {
	t.Parallel()
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: tools.SurfaceImplementInvestigate}, 1)
	contractcheck.FailErr(t, "CompileToolPlan", err)
	got := plan.DeferredNames()
	sortStrings(got)
	want := append([]string(nil), implementInvestigateDeferredTools...)
	sortStrings(want)
	if !sliceEqual(got, want) {
		t.Fatalf("implement_investigate deferred = %v want %v", got, want)
	}
}

func TestCoordinatorToolSurface_investigateSurfaceRegistered(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	if _, ok := surfaces[tools.SurfaceImplementInvestigate]; !ok {
		t.Fatal("missing implement_investigate surface registration")
	}
}

func TestCoordinatorToolSurface_investigateProductWriteScopeLoaded(t *testing.T) {
	t.Parallel()
	reg, err := sandbox.LoadPathScopes()
	contractcheck.FailErr(t, "LoadPathScopes", err)
	scope, ok := reg[tools.CoordinatorProductWriteScope]
	if !ok {
		t.Fatal("missing coordinator_product_write scope")
	}
	if len(scope.Write) == 0 {
		t.Fatalf("coordinator_product_write incomplete: write=%v", scope.Write)
	}
	if len(scope.Deny) != 0 {
		t.Fatalf("coordinator_product_write must have no heuristic deny list: deny=%v", scope.Deny)
	}
	if err := sandbox.CheckWriteInScope(scope, tools.CoordinatorProductWriteScope, "src/foo.go"); err != nil {
		t.Fatalf("product path: %v", err)
	}
	if err := sandbox.CheckWriteInScope(scope, tools.CoordinatorProductWriteScope, settingsoverlay.DirName()+"/blueprints/x.md"); err != nil {
		t.Fatalf("plan path: %v", err)
	}
	// Overlay trust surfaces and dependency trees are not denied by write scope;
	// overlay writes reach the agent-policy gate for approval.
	if err := sandbox.CheckWriteInScope(scope, tools.CoordinatorProductWriteScope, settingsoverlay.DirName()+"/rules/x.yaml"); err != nil {
		t.Fatalf("overlay rules must reach approval: %v", err)
	}
	if err := sandbox.CheckWriteInScope(scope, tools.CoordinatorProductWriteScope, settingsoverlay.DirName()+"/ignores.yaml"); err != nil {
		t.Fatalf("overlay ignores must reach approval: %v", err)
	}
	if err := sandbox.CheckWriteInScope(scope, tools.CoordinatorProductWriteScope, "node_modules/pkg/index.js"); err != nil {
		t.Fatalf("node_modules write must not be denied by scope: %v", err)
	}
	if err := sandbox.CheckWriteInScope(scope, tools.CoordinatorProductWriteScope, "vendor/pkg/foo.go"); err != nil {
		t.Fatalf("vendor write must not be denied by scope: %v", err)
	}
}

func TestCoordinatorToolSurface_investigateSurveyRouteRendered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	vars := map[string]any{"execution_mode": "investigate"}
	if err := prompts.MergeCoordinatorSurfacePathVars(tools.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	rendered := renderCoordinatorTripartiteForRunContext(
		t, root, api.CoordinatorRunContext{}, nil, "Research this repo", nil,
		tools.SurfaceImplementInvestigate,
	)
	for _, want := range []string{
		"`summarize`",
		"list_dir",
		"load through `request_tools`",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q in rendered investigate prompt", want)
		}
	}
	for _, forbidden := range []string{
		"Lane S or",
		"Lane A",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("investigate prompt must not contain %q", forbidden)
		}
	}
	modeRendered, err := engine.Render(context.Background(), "agents/coordinator-mode-implement-investigate.md", vars)
	contractcheck.FailErr(t, "render mode", err)
	if !strings.Contains(modeRendered, "INVEST_HANDLE_NOT_OBSERVED") {
		t.Fatal("investigate mode partial missing grounding hints")
	}
	if strings.Contains(modeRendered, "COORDINATOR_INVESTIGATE_DENIED_PATH") {
		t.Fatal("investigate mode must not contain obsolete COORDINATOR_INVESTIGATE_DENIED_PATH")
	}
}

func sortStrings(in []string) {
	for i := 0; i < len(in); i++ {
		for j := i + 1; j < len(in); j++ {
			if in[j] < in[i] {
				in[i], in[j] = in[j], in[i]
			}
		}
	}
}
