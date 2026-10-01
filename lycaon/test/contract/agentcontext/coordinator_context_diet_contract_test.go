package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorContextDietTurnProfileMatrix(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	for _, row := range ContextDietMatrix {
		t.Run(row.Name, func(t *testing.T) {
			t.Parallel()
			runCtx := surface.EnrichRunContextForWorkflow(row.RunCtx, root)
			profile := surface.ResolveTurnProfile(runCtx, row.Sess, coordinatorTurnHistory(row.History, row.UserPrompt), row.State)
			if profile.SurfaceID != row.SurfaceID {
				t.Fatalf("surface = %q want %q", profile.SurfaceID, row.SurfaceID)
			}
			if len(profile.ModeRefs) > 2 {
				t.Fatalf("len(modeRefs)=%d want ≤2", len(profile.ModeRefs))
			}
			if row.MaxModes > 0 && len(profile.ModeRefs) > row.MaxModes {
				t.Fatalf("modeRefs = %v", profile.ModeRefs)
			}
			if len(row.ModeRefs) > 0 && !sliceEqual(profile.ModeRefs, row.ModeRefs) {
				t.Fatalf("modeRefs = %v want %v", profile.ModeRefs, row.ModeRefs)
			}
		})
	}
}

func TestCoordinatorContextDietSurfaceToolsSSOT(t *testing.T) {
	t.Parallel()
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "CompileToolPlans", err)
	surfaces := make(map[string][]string, len(plans))
	for id, plan := range plans {
		surfaces[id] = plan.ImmediateNames()
	}
	for _, tool := range []string{"scan_pack", "scan_query", "render_view"} {
		if !contractcheck.ContainsString(surfaces["implement_synthesis"], tool) {
			t.Fatalf("implement_synthesis missing %q", tool)
		}
	}
	if !contractcheck.ContainsString(surfaces["implement_routing"], "scan_query") {
		t.Fatal("implement_routing must expose scan query for host scan on project open")
	}
	if !contractcheck.ContainsString(surfaces["implement_routing"], "list_dir") {
		t.Fatal("implement_routing must expose list_dir for repo-root orientation")
	}
	if contractcheck.ContainsString(surfaces["implement_routing"], "read") {
		t.Fatal("implement_routing must not expose read — use list_dir on routing turns; read is synthesis-only")
	}
	if !contractcheck.ContainsString(surfaces["implement_routing"], "promote_overlay") {
		t.Fatal("implement_routing must expose promote_overlay for pending overlay promotion")
	}
	for _, tool := range []string{"task", "worker_cancel", "answer_decision", "extend_worker_budget", "decline_worker_budget", "update_progress", "promote_overlay", "reject_overlay", "wait", "ask_user"} {
		if !contractcheck.ContainsString(surfaces["implement_dispatch"], tool) {
			t.Fatalf("implement_dispatch missing %q", tool)
		}
	}
	for _, forbidden := range []string{"read", "grep", "pack_board", "list_dir", "delegate_dispatch"} {
		if contractcheck.ContainsString(surfaces["implement_dispatch"], forbidden) {
			t.Fatalf("implement_dispatch must not expose %q", forbidden)
		}
	}
	promote := surfaces["implement_overlay_promote"]
	for _, tool := range []string{"promote_overlay", "reject_overlay", "preview_overlay", "worker_cancel", "answer_decision", "extend_worker_budget", "decline_worker_budget", "update_progress", "task", "wait", "ask_user"} {
		if !contractcheck.ContainsString(promote, tool) {
			t.Fatalf("implement_overlay_promote missing %q", tool)
		}
	}
	for _, forbidden := range []string{"edit", "replace_lines", "delegate_dispatch"} {
		if contractcheck.ContainsString(promote, forbidden) {
			t.Fatalf("implement_overlay_promote must not expose %q", forbidden)
		}
	}
	for _, id := range []string{"plan_stub", "plan_research", "plan_approve", "await_user", "plan_execute"} {
		if len(surfaces[id]) == 0 {
			t.Fatalf("missing tools for %q", id)
		}
	}
	awaitTools := surfaces["await_user"]
	for _, tool := range []string{"ask_user", "wait", "workflow_advance"} {
		if !contractcheck.ContainsString(awaitTools, tool) {
			t.Fatalf("await_user missing required tool %q", tool)
		}
	}
	for _, forbidden := range []string{"write", "edit", "replace_lines"} {
		if contractcheck.ContainsString(awaitTools, forbidden) {
			t.Fatalf("await_user must not expose product-write tool %q", forbidden)
		}
	}
	synthesis := surfaces["implement_synthesis"]
	for _, forbidden := range []string{"task", "write", "edit", "command", "wait", "pack_board"} {
		if contractcheck.ContainsString(synthesis, forbidden) {
			t.Fatalf("implement_synthesis must not expose %q", forbidden)
		}
	}
	if !contractcheck.ContainsString(synthesis, "update_progress") {
		t.Fatal("implement_synthesis must expose update_progress for checklist reconciliation")
	}
	park := surfaces["implement_park"]
	for _, tool := range []string{"task", "pack_board"} {
		if !contractcheck.ContainsString(park, tool) {
			t.Fatalf("implement_park must expose %q for partial dispatch repair", tool)
		}
	}
	// The wait loop re-enters on every worker event, so the park floor stays
	// closed: nothing loadable, no request_tools, no eager MCP.
	for _, forbidden := range []string{"read", "grep", "promote_overlay", "delegate_dispatch", "request_tools"} {
		if contractcheck.ContainsString(park, forbidden) {
			t.Fatalf("implement_park floor must not carry %q", forbidden)
		}
	}
	investigate := surfaces["implement_investigate"]
	for _, tool := range []string{"read", "grep", "list_dir", "skills_read", "request_tools", "ask_user"} {
		if !contractcheck.ContainsString(investigate, tool) {
			t.Fatalf("implement_investigate floor must carry %q", tool)
		}
	}
	// Everything that mutates, runs, or delegates loads per turn.
	for _, loadable := range []string{"write", "command", "git_status", "task", "capture_page"} {
		if contractcheck.ContainsString(investigate, loadable) {
			t.Fatalf("implement_investigate floor must not carry loadable tool %q", loadable)
		}
	}
}

func TestRenderViewSchemaEncouragesMockupsAndExplanatoryVisuals(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	meta, ok := cfg.ToolMeta("render_view")
	if !ok {
		t.Fatal("missing render_view schema")
	}
	for _, want := range []string{"UI/product mockups", "especially before", "diagrams", "runtime evidence"} {
		if !strings.Contains(meta.Description, want) {
			t.Fatalf("render_view description missing %q: %s", want, meta.Description)
		}
	}
}

func TestTrimCoordinatorToolMetaPreservesRequiredFields(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	meta, ok := cfg.ToolMeta("read")
	if !ok {
		t.Fatal("missing read schema")
	}
	full := tools.ToolMeta{Name: "read", Description: meta.Description, ArgsSchema: meta.ArgsSchema}
	trimmed := tools.TrimCoordinatorToolMeta(full)
	if trimmed.ArgsSchema == nil {
		t.Fatal("trimmed schema nil")
	}
	if trimmed.ArgsSchema["properties"] == nil {
		t.Fatal("trimmed schema missing properties")
	}
	if len(trimmed.Description) == 0 {
		t.Fatal("trimmed description empty")
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
