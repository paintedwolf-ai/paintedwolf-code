package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const (
	surfaceImplementRouting        = "implement_routing"
	surfaceImplementDispatch       = "implement_dispatch"
	surfaceImplementSynthesis      = "implement_synthesis"
	surfaceImplementOverlayPromote = "implement_overlay_promote"
	surfaceImplementPark           = "implement_park"
)

// orchestrationLifecycleTools remain callable on pinned implement surfaces.
var orchestrationLifecycleTools = []string{
	"answer_decision",
	"decline_worker_budget",
	"extend_worker_budget",
	"update_progress",
	"worker_cancel",
	"wait",
}

// orchestrationLifecyclePromptTools appear in pinned-surface prompts.
var orchestrationLifecyclePromptTools = []string{
	"answer_decision",
	"extend_worker_budget",
	"update_progress",
}

// orchestrationPinnedSurfaces remain active while parallel work is open.
var orchestrationPinnedSurfaces = []string{
	surfaceImplementOverlayPromote,
	surfaceImplementPark,
}

// implementPromptToolsNotOnWire explains prompt-only tool references.
var implementPromptToolsNotOnWire = map[string]string{
	"summarize":                      "survey-first-pass partial (tool name in backticks); investigate/synthesis/overlay-promote read surfaces own the tool",
	"write":                          "product mutations via task() — not coordinator wire",
	"edit":                           "product mutations via task() — not coordinator wire",
	"replace_lines":                  "product mutations via task() — not coordinator wire",
	"restore_version":                "product mutations via task() — not coordinator wire",
	"command":                        "investigate/overlay_promote compile the family",
	"command_output":                 "family compile; live jobs imply it elsewhere",
	"command_stop":                   "family compile; live jobs imply it elsewhere",
	"held_result":                    "resource-implied control; a held call makes it callable on every surface",
	"held_stop":                      "resource-implied control; a held call makes it callable on every surface",
	"verify":                         "host-side test runner — surface override at invoke on investigate/overlay_promote",
	"git_status":                     "host/repo orientation — not on implement surface SSOT",
	"git_diff":                       "host/repo orientation — not on implement surface SSOT",
	"preview_overlay":                "landing is the overlay-promote surface's; other surfaces name it describing where a write leg ends up",
	"reject_overlay":                 "landing is the overlay-promote surface's; other surfaces name it describing where a write leg ends up",
	"delete":                         "orchestrate surfaces delegate destructive I/O via task()",
	"wc":                             "orchestrate surfaces delegate via task() — investigate has native wc",
	"stat":                           "orchestrate surfaces delegate via task() — investigate has native stat",
	"chmod":                          "orchestrate surfaces delegate via task()",
	"code_rewrite":                   "orchestrate surfaces delegate via task()",
	"git_commit":                     "orchestrate surfaces delegate commits via task()",
	"git_restore":                    "orchestrate surfaces delegate via task()",
	"git_log":                        "orchestrate surfaces delegate git history via task()",
	"git_show":                       "orchestrate surfaces delegate via task()",
	"git_blame":                      "orchestrate surfaces delegate via task()",
	"source_history":                 "orchestrate surfaces delegate via task()",
	"git_ref":                        "orchestrate surfaces delegate via task()",
	"git_branches":                   "orchestrate surfaces delegate via task()",
	"delegate_dispatch":              "workflow delegation legs only (plan_execute) — ambient implement dispatch is task()",
	"workflow_advance":               "workflow runtime visibility",
	"workflow_transition":            "workflow runtime visibility",
	"fanout_plan":                    "orchestrate_plan surface only — coordinator-planned fanout stamp",
	"workflow_catalog_summaries":     "compose/catalog axis only",
	"workflow_compose":               "compose axis only",
	"workflow_compose_from_template": "compose axis only",
	"workflow_persist":               "compose axis only",
	"workflow_user_feedback":         "workflow runtime visibility",
}

// specialistSpawnAgents are task targets, not wire tools.
var specialistSpawnAgents = []string{
	"security-reviewer",
	"implementer",
}

func loadImplementSurfaces(t *testing.T) map[string][]string {
	t.Helper()
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "CompileToolPlans", err)
	surfaces := make(map[string][]string, len(plans))
	for id, plan := range plans {
		surfaces[id] = plan.ImmediateNames()
	}
	return surfaces
}

// routingPromptToolsAllowedOffWire documents shared prompt references.
var routingPromptToolsAllowedOffWire = map[string]string{
	"read":            "synthesis surface only — routing uses list_dir for repo orientation",
	"grep":            "Lane S sync tools in surface-build; routing wire is list_dir subset",
	"survey_repo":     "survey-first-pass partial; investigate/synthesis surfaces own bundle survey",
	"summarize":       "survey-first-pass partial; investigate/synthesis/overlay-promote surfaces own the briefing tool",
	"find":            "Lane S sync tools in surface-build; routing wire is list_dir subset",
	"scan_pack":       "core scan policy; routing uses native scan_* drill-down",
	"reject_overlay":  "overlay-promote / synthesis surfaces own reject; routing prompts describe the model but routing itself dispatches via promote_overlay or task()",
	"preview_overlay": "overlay-promote / synthesis surfaces own preview; routing prompts describe the model but routing itself doesn't pre-flight overlays",
	"answer_decision": "routing dispatches; needs_decision resolution on pinned surfaces while workers run",
}

// orchestrateSharedPromptToolsAllowedOffWire documents shared prompt references.
var orchestrateSharedPromptToolsAllowedOffWire = map[string]string{
	"fetch_url":   "coordinator-core external-facts invariant; Lane S surfaces own fetch",
	"find":        "coordinator-core / surface-build Lane S copy; pinned surfaces stay lean",
	"grep":        "coordinator-core / surface-build Lane S copy; pinned surfaces stay lean",
	"survey_repo": "survey-first-pass partial; investigate/synthesis surfaces own bundle survey",
	"list_dir":    "routing/synthesis survey; overlay_promote uses list_dir/read",
	"read":        "Lane S spot-check on overlay-promote; park is lifecycle-only",
	"scan_pack":   "core scan policy; orchestrate surfaces use native scan_* drill-down",
	"web_search":  "coordinator-core external-facts invariant; Lane S surfaces own search",
}

// orchestratePromptToolsAllowedOffWire documents surface-specific omissions.
var orchestratePromptToolsAllowedOffWire = map[string]string{
	"pack_board":      "dispatch/park mode copy — pack_board on routing/overlay-promote only",
	"preview_overlay": "worker-chain baseline describes integration; overlay-promote surface provides preview",
	"promote_overlay": "worker-chain baseline describes integration; overlay-promote surface provides promote",
	"reject_overlay":  "routing/dispatch describe integration; overlay-promote provides reject",
	"task":            "park is host-cycle subscribe only — task() resumes after siblings idle",
}

func mergeToolAllowlists(maps ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// fileText reads an embedded or linked pack file.
func fileText(t *testing.T, at extpacks.Source) string {
	t.Helper()
	data, err := at.Read()
	contractcheck.FailErr(t, "read "+at.String(), err)
	return string(data)
}

func coordinatorProfileGrantsTool(profile sandbox.ToolProfile, tool string) bool {
	return profile.Tools[tool]
}

func toolOnImplementSurface(surfaceTools []string, tool string) bool {
	return contractcheck.ContainsString(surfaceTools, tool)
}
