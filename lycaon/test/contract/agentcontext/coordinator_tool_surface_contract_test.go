package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var implementInvestigateSurfaceTools = []string{
	"command", "source_history", "skills_read",
	"update_progress", "ask_user", "request_tools",
	"recall",
}

func TestCoordinatorToolSurfaceSecretsRemainRequestableWithOutboundExecution(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: toolcontract.SurfaceImplementInvestigate}, 1)
	contractcheck.FailErr(t, "CompileToolPlan", err)
	deferred := plan.DeferredNames()
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load actual command effect declarations", err)
	network, err := toolcontract.NewCommandEffects(toolcontract.CommandNetworkEgress)
	contractcheck.FailErr(t, "declare network effect", err)
	for _, id := range []string{toolcontract.SurfaceImplementInvestigate, "implement_overlay_promote"} {
		row, err := catalog.Surface(id)
		contractcheck.FailErr(t, "load outbound surface", err)
		effects, err := toolcontract.NewCommandEffects(row.CommandEffects...)
		contractcheck.FailErr(t, "compile outbound effects", err)
		if !contractcheck.ContainsString(row.FloorTools(), "command") || !effects.Allows(network) {
			t.Fatalf("%s must provide native outbound command capability", id)
		}
	}

	for _, name := range []string{"secret_generate", "secret_list", "secret_revoke"} {
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
		if !prompts.CoordinatorSurfaceMutationCapable(surfaceID) {
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
	"command_output",
	"command_stop",
	"restore_version",
	"verify",
	"wait",
	"terminal_open",
	"terminal_send",
	"terminal_read",
	"terminal_snapshot",
	"terminal_close",
	"render_view",
	"view_image",
	"view_video",
	"capture_page",
	"measure_page",
	"page_open",
	"page_act",
	"page_snapshot",
	"page_close",
	"secret_generate",
	"secret_list",
	"secret_revoke",
	"web_search",
	"fetch_url",
	"scan_pack",
	"scan_query",
	"scan_list",
	"scan_summary",
	"scan_compare",
	"task",
	"pack_board",
	"worker_cancel",
	"answer_decision",
	"extend_worker_budget",
	"decline_worker_budget",
}

func TestCoordinatorToolSurface_implementInvestigateMatchesSSOT(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	got := append([]string(nil), surfaces[toolcontract.SurfaceImplementInvestigate]...)
	sortStrings(got)
	want := append([]string(nil), implementInvestigateSurfaceTools...)
	sortStrings(want)
	if !sliceEqual(got, want) {
		t.Fatalf("implement_investigate tools = %v want %v", got, want)
	}
}

func TestCoordinatorToolSurface_implementInvestigateDeferred(t *testing.T) {
	t.Parallel()
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: toolcontract.SurfaceImplementInvestigate}, 1)
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
	if _, ok := surfaces[toolcontract.SurfaceImplementInvestigate]; !ok {
		t.Fatal("missing implement_investigate surface registration")
	}
}

func TestCoordinatorToolSurface_investigateProductWriteScopeLoaded(t *testing.T) {
	t.Parallel()
	reg, err := sandbox.LoadPathScopes()
	contractcheck.FailErr(t, "LoadPathScopes", err)
	scope, ok := reg[toolcontract.CoordinatorProductWriteScope]
	if !ok {
		t.Fatal("missing coordinator_product_write scope")
	}
	if len(scope.Write) == 0 {
		t.Fatalf("coordinator_product_write incomplete: write=%v", scope.Write)
	}
	if len(scope.Deny) != 0 {
		t.Fatalf("coordinator_product_write must have no heuristic deny list: deny=%v", scope.Deny)
	}
	if err := sandbox.CheckWriteInScope(scope, toolcontract.CoordinatorProductWriteScope, "src/foo.go"); err != nil {
		t.Fatalf("product path: %v", err)
	}
	if err := sandbox.CheckWriteInScope(scope, toolcontract.CoordinatorProductWriteScope, settingsoverlay.DirName()+"/blueprints/x.md"); err != nil {
		t.Fatalf("plan path: %v", err)
	}
	// Overlay trust surfaces and dependency trees are not denied by write scope;
	// overlay writes reach the agent-policy gate for approval.
	if err := sandbox.CheckWriteInScope(scope, toolcontract.CoordinatorProductWriteScope, settingsoverlay.DirName()+"/rules/x.yaml"); err != nil {
		t.Fatalf("overlay rules must reach approval: %v", err)
	}
	if err := sandbox.CheckWriteInScope(scope, toolcontract.CoordinatorProductWriteScope, settingsoverlay.DirName()+"/ignores.yaml"); err != nil {
		t.Fatalf("overlay ignores must reach approval: %v", err)
	}
	if err := sandbox.CheckWriteInScope(scope, toolcontract.CoordinatorProductWriteScope, "node_modules/pkg/index.js"); err != nil {
		t.Fatalf("node_modules write must not be denied by scope: %v", err)
	}
	if err := sandbox.CheckWriteInScope(scope, toolcontract.CoordinatorProductWriteScope, "vendor/pkg/foo.go"); err != nil {
		t.Fatalf("vendor write must not be denied by scope: %v", err)
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
