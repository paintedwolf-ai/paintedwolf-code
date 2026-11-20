// Package surfacematrix holds the surface matrix: one table of the tools
// each coordinator surface and each tool profile carries and must not carry.
package surfacematrix

import (
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// Family is the surface-matrix rule table.
var Family = invariants.Family{Name: "surface-matrix", Invariants: []invariants.Invariant{
	Surfaces("coordinator-surfaces-carry-their-tools", surfaces),
	{Name: "synthesis-is-not-a-routing-superset", Anchors: []invariants.Anchor{surfaceCompiler}, Check: synthesisIsNotARoutingSuperset},
	Profiles("tool-profiles-carry-their-tools", profiles),
}}

var (
	surfaceCompiler = invariants.Func("internal/coordinator/surface", "CompileToolPlan")
	profileLoader   = invariants.Func("internal/sandbox", "LoadToolProfiles")
)

// orchestration are the lifecycle tools a surface that runs workers keeps.
var orchestration = []string{"answer_decision", "update_progress", "worker_cancel", "wait"}

// SurfaceRow is what one coordinator surface's immediate tools must hold and
// must not.
type SurfaceRow struct {
	ID                string
	Requires, Forbids []string
}

var surfaces = []SurfaceRow{
	{
		ID: "implement_routing",
		// command orients at the repository root; scan_query reads the host
		// scan on project open; promote_overlay settles pending overlays.
		Requires: []string{"command", "scan_query", "promote_overlay", "task"},
		Forbids:  []string{"read", "delegate_dispatch"},
	},
	{
		ID:       "implement_dispatch",
		Requires: append([]string{"task", "promote_overlay", "reject_overlay", "ask_user"}, orchestration...),
		Forbids:  []string{"read", "grep", "pack_board", "list_dir", "delegate_dispatch", "command"},
	},
	{
		ID: "implement_overlay_promote",
		// Integration runs through promote_overlay and reject_overlay, or
		// task for partial legs, never a direct edit.
		Requires: append([]string{"promote_overlay", "reject_overlay", "preview_overlay", "task", "ask_user", "command"}, orchestration...),
		Forbids:  []string{"edit", "replace_lines", "delegate_dispatch"},
	},
	{
		ID: "implement_synthesis",
		// Wrap-up is read-only and reports; it reconciles the checklist.
		Requires: []string{"command", "scan_pack", "scan_query", "render_view", "update_progress"},
		Forbids:  []string{"task", "write", "edit", "wait", "pack_board", "worker_cancel", "answer_decision", "promote_overlay", "reject_overlay", "preview_overlay"},
	},
	{
		ID: "implement_park",
		// The wait loop re-enters on every worker event, so the park floor
		// stays closed: nothing loadable, no request_tools.
		Requires: append([]string{"task", "pack_board"}, orchestration...),
		Forbids:  []string{"read", "grep", "promote_overlay", "delegate_dispatch", "request_tools", "command"},
	},
	{
		ID: "implement_investigate",
		// Worker dispatch and page capture stay deferred.
		Requires: []string{"command", "skills_read", "request_tools", "ask_user"},
		Forbids:  []string{"write", "read", "grep", "list_dir", "git_status", "task", "capture_page"},
	},
	{ID: "await_user", Requires: []string{"ask_user", "wait", "workflow_advance"}, Forbids: []string{"write", "edit", "replace_lines"}},
	{ID: "recon_reconcile", Requires: []string{"workflow_transition"}, Forbids: []string{"task", "fanout_plan", "workflow_advance"}},
	{ID: "workflow_compose", Requires: []string{"workflow_compose", "workflow_persist", "state_start"}},
	{ID: "plan_stub"},
	{ID: "plan_research"},
	{ID: "plan_approve"},
	{ID: "plan_execute"},
}

// immediate is a surface's immediate tool names with one workspace root.
func immediate(r *invariants.Run, id string) []string {
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: id}, 1)
	if err != nil {
		r.Fatalf("compile the %s tool plan: %v", id, err)
	}
	return plan.ImmediateNames()
}

// Surfaces is a rule that each row's surface carries its required tools and
// none of its forbidden ones.
func Surfaces(name string, rows []SurfaceRow) invariants.Invariant {
	return invariants.Invariant{Name: name, Anchors: []invariants.Anchor{surfaceCompiler}, Check: func(r *invariants.Run) {
		for _, row := range rows {
			tools := immediate(r, row.ID)
			if len(tools) == 0 {
				r.Report("%s has no tools", row.ID)
			}
			for _, tool := range row.Requires {
				if !slices.Contains(tools, tool) {
					r.Report("%s does not carry %s", row.ID, tool)
				}
			}
			for _, tool := range row.Forbids {
				if slices.Contains(tools, tool) {
					r.Report("%s carries %s", row.ID, tool)
				}
			}
		}
	}}
}

// Read-only wrap-up cannot hold every routing tool.
func synthesisIsNotARoutingSuperset(r *invariants.Run) {
	synthesis := immediate(r, "implement_synthesis")
	for _, tool := range immediate(r, "implement_routing") {
		if !slices.Contains(synthesis, tool) {
			return
		}
	}
	r.Report("implement_synthesis carries every implement_routing tool")
}

// ProfileRow is what one tool profile grants, keeps eager, keeps deferred and
// denies, and the command effects it allows.
type ProfileRow struct {
	ID                              string
	Grants, Eager, Deferred, Denied []string
	Effects                         []toolcontract.CommandEffectClass
}

var profiles = []ProfileRow{
	{
		ID:       "implement",
		Grants:   []string{"restore_version", "source_history", "command", "scan_list", "scan_summary", "scan_query", "scan_pack", "scan_compare"},
		Eager:    []string{"command", "command_output", "command_stop", "verify", "complete_leg", "request_tools", "skills_read", "terminal_open", "terminal_close", "source_history"},
		Deferred: []string{"page_open", "page_act", "page_close", "capture_page", "scan_pack", "secret_generate", "secret_list", "secret_revoke", "handoff_reserve"},
		Denied:   []string{"edit", "replace_lines", "jq_edit", "delegate_start", "state_set", "write", "read", "grep", "find", "list_dir", "jq", "http_request"},
		Effects:  []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress},
	},
	{
		ID:       "explore_readonly",
		Grants:   []string{"command"},
		Eager:    []string{"command", "record_finding", "complete_leg", "request_tools", "skills_read"},
		Deferred: []string{"page_open", "page_act", "page_close", "capture_page", "scan_query", "source_history"},
		Denied:   []string{"edit", "replace_lines", "code_rewrite", "jq_edit", "write", "git_commit", "secret_generate", "read", "grep", "find", "list_dir", "jq", "git_status", "git_diff", "git_log", "git_show"},
		Effects:  []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly},
	},
	{ID: "coordinator", Grants: []string{"restore_version", "source_history", "command"}},
	{ID: "worker_readonly", Grants: []string{"command"}},
	{ID: "plan_review_readonly", Grants: []string{"command"}},
	{ID: "plan_write_only", Grants: []string{"command"}},
}

// Profiles is a rule that each row's tool profile matches it.
func Profiles(name string, rows []ProfileRow) invariants.Invariant {
	return invariants.Invariant{Name: name, Anchors: []invariants.Anchor{profileLoader}, Check: func(r *invariants.Run) {
		loaded, err := sandbox.LoadToolProfiles()
		r.Must(err)
		byID := map[string]sandbox.ToolProfile{}
		for _, p := range loaded {
			byID[p.ID] = p
		}
		for _, row := range rows {
			p, ok := byID[row.ID]
			if !ok {
				r.Report("tool profile %s is not declared", row.ID)
				continue
			}
			for _, tool := range row.Grants {
				if !p.Tools[tool] {
					r.Report("%s does not grant %s", row.ID, tool)
				}
			}
			for _, tool := range row.Eager {
				if !p.ToolAllowed(tool) || !p.ToolSticky(tool) {
					r.Report("%s does not keep %s eager", row.ID, tool)
				}
			}
			for _, tool := range row.Deferred {
				if !p.ToolAllowed(tool) || !p.ToolDeferred(tool) {
					r.Report("%s does not keep %s requestable", row.ID, tool)
				}
			}
			for _, tool := range row.Denied {
				if p.ToolAllowed(tool) {
					r.Report("%s allows %s", row.ID, tool)
				}
			}
			if row.Effects != nil {
				want, err := toolcontract.NewCommandEffects(row.Effects...)
				r.Must(err)
				if p.CommandEffects != want {
					r.Report("%s allows command effects %v, want %v", row.ID, p.CommandEffects.Classes(), want.Classes())
				}
			}
		}
	}}
}
