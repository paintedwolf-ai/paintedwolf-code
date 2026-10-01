package contract

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// promptBudgetCategory describes one table in config/packs/painted-wolf/platform/host/prompt-budgets.yaml.
type promptBudgetCategory struct {
	Title       string
	Description string
	CapPath     string // yaml key under version:
	RefreshCmd  string
}

const promptBudgetRefreshCmd = "UPDATE_PROMPT_BUDGETS=1 ./task test:digest -- ./test/contract/... -run TestRenderedPromptsWithinBudget"

var promptBudgetCategories = map[string]promptBudgetCategory{
	"worker_personas": {
		Title:       "worker_personas",
		Description: "Rendered worker persona stack (archetype + delta + playbooks + host WorkerPromptContext) per agent id in _persona-contract.yaml.",
		CapPath:     "worker_personas.<agent_id>",
		RefreshCmd:  promptBudgetRefreshCmd,
	},
	"coordinator_tripartite": {
		Title:       "coordinator_tripartite",
		Description: "Full tripartite coordinator system prompt: coordinator-core + mode partial + posture surface template, rendered for ContextDietMatrix fixtures (worst-case compile size per turn kind).",
		CapPath:     "coordinator_tripartite.<fixture_name>",
		RefreshCmd:  promptBudgetRefreshCmd,
	},
	"coordinator_injects": {
		Title:       "coordinator_injects",
		Description: "Ephemeral inject blocks appended per turn — measured via PromptBudgetInjectMatrix fixtures.",
		CapPath:     "coordinator_injects.<inject_id>",
		RefreshCmd:  promptBudgetRefreshCmd,
	},
	"agent_templates": {
		Title:       "agent_templates",
		Description: "Non-worker agent system prompt templates from config/packs/painted-wolf/platform/agents/*.yaml (e.g. coordinator core template before tripartite assembly).",
		CapPath:     "agent_templates.<profile_id>",
		RefreshCmd:  promptBudgetRefreshCmd,
	},
	"kicks": {
		Title:       "kicks",
		Description: "Host kick nudge templates under config/packs/painted-wolf/platform/guidance/*.md (discovered from disk; includes parsed partials).",
		CapPath:     "kicks.<kick_id>",
		RefreshCmd:  promptBudgetRefreshCmd,
	},
	"tool_surfaces": {
		Title:       "tool_surfaces",
		Description: "Wire-facing LLM tool definition bytes per tool profile (deferred tools excluded; coordinator metas trimmed). Caps the upfront schema cost a profile puts on every request.",
		CapPath:     "tool_surfaces.<profile_id>",
		RefreshCmd:  promptBudgetRefreshCmd,
	},
}

// promptBudgetEntry documents one capped artifact for failure digests.
type promptBudgetEntry struct {
	Measures  string
	Fixture   string
	TrimPaths []string
	BumpNote  string
}

// buildPromptBudgetCatalog derives per-id documentation from the same registry as measurement.
func buildPromptBudgetCatalog(reg *PromptBudgetRegistry) map[string]map[string]promptBudgetEntry {
	if reg == nil {
		return nil
	}
	tripartite := make(map[string]promptBudgetEntry, len(reg.Tripartite))
	for _, row := range reg.Tripartite {
		tripartite[row.Name] = tripartiteEntryForRow(row)
	}
	injects := make(map[string]promptBudgetEntry, len(reg.Injects))
	for _, spec := range reg.Injects {
		injects[spec.ID] = injectBudgetEntry(reg.LycaonRoot, spec)
	}
	kicks := make(map[string]promptBudgetEntry, len(reg.KickIDs))
	for _, id := range reg.KickIDs {
		kicks[id] = kickBudgetEntry(reg.LycaonRoot, id)
	}
	workerPersonas := make(map[string]promptBudgetEntry, len(reg.WorkerPersonas))
	for id, def := range reg.WorkerPersonas {
		workerPersonas[id] = workerPersonaBudgetEntry(reg.LycaonRoot, id, reg.AgentTemplates[id], def)
	}
	agentTemplates := make(map[string]promptBudgetEntry, len(reg.AgentTemplates))
	for id, ref := range reg.AgentTemplates {
		agentTemplates[id] = agentTemplateBudgetEntry(reg.LycaonRoot, id, ref)
	}
	return map[string]map[string]promptBudgetEntry{
		"worker_personas":        workerPersonas,
		"coordinator_tripartite": tripartite,
		"coordinator_injects":    injects,
		"agent_templates":        agentTemplates,
		"kicks":                  kicks,
		"tool_surfaces":          toolSurfaceBudgetEntries(reg.LycaonRoot, reg.ToolProfileIDs()),
	}
}

func injectBudgetEntry(lycaonRoot string, spec PromptBudgetInjectSpec) promptBudgetEntry {
	return promptBudgetEntry{
		Measures:  fmt.Sprintf("Coordinator inject %q (%s).", spec.ID, spec.Template),
		Fixture:   spec.Fixture,
		TrimPaths: promptTemplateTrimPaths(lycaonRoot, spec.Template, spec.ExtraTrim...),
		BumpNote:  spec.BumpNote,
	}
}

func kickBudgetEntry(lycaonRoot, id string) promptBudgetEntry {
	rel := filepath.ToSlash(filepath.Join("kicks", id+".md"))
	trim := promptTemplateTrimPaths(lycaonRoot, rel)
	if len(trim) == 0 {
		if path, err := catalogfixture.StockGuidancePath(id); err == nil {
			trim = []string{checkoutPath(lycaonRoot, path.String())}
		}
	}
	return promptBudgetEntry{
		Measures:  "Host kick template " + rel,
		Fixture:   "Render " + rel + " with coordinator template vars",
		TrimPaths: trim,
	}
}

func workerPersonaBudgetEntry(lycaonRoot, id, templateRef string, def prompts.AgentPersonaDef) promptBudgetEntry {
	trim := promptTemplateTrimPaths(lycaonRoot, templateRef)
	for _, pb := range def.PlaybookIDs {
		trim = append(trim, filepath.ToSlash(filepath.Join("lycaon", "config", "packs", "painted-wolf", "platform", "playbooks", pb+".yaml")))
	}
	sort.Strings(trim)
	trim = contractcheck.DedupeStrings(trim)
	return promptBudgetEntry{
		Measures:  "Rendered persona for worker agent " + id + " (_persona-contract.yaml).",
		Fixture:   "RenderPersona(" + id + ")",
		TrimPaths: trim,
		BumpNote:  "Persona/playbook growth — keep worker prompts focused; avoid duplicating coordinator policy in worker personas.",
	}
}

func agentTemplateBudgetEntry(lycaonRoot, id, ref string) promptBudgetEntry {
	trim := promptTemplateTrimPaths(lycaonRoot, ref, filepath.ToSlash(filepath.Join("lycaon", "config", "packs", "painted-wolf", "platform", "agents", id+".yaml")))
	return promptBudgetEntry{
		Measures:  "Agent system template for profile " + id,
		Fixture:   "config/packs/painted-wolf/platform/agents/" + id + ".yaml system_prompt_template → " + ref,
		TrimPaths: trim,
	}
}

func tripartiteEntryForRow(row ContextDietMatrixRow) promptBudgetEntry {
	fixture := "surface=" + row.SurfaceID
	if len(row.ModeRefs) > 0 {
		fixture += " | mode=" + row.ModeRefs[0]
		if len(row.ModeRefs) > 1 {
			fixture += "+" + row.ModeRefs[1]
		}
	}
	switch row.UserPrompt {
	case "":
		fixture += " | user=(empty)"
	case "Research this repo":
		fixture += " | visible user first turn"
	case "Explain auth":
		fixture += " | visible user follow-up"
	default:
		if row.UserPrompt == surface.HostLoopWakeSentinel {
			fixture += " | host loop-wake"
		} else {
			fixture += " | user=" + row.UserPrompt
		}
	}
	if len(row.History) > 0 {
		fixture += " | history=worker envelope fixture"
	}
	if len(row.State.PendingOverlayIDs) > 0 {
		fixture += " | pending_overlay_promote"
	}
	if row.RunCtx.HasComposeDraft {
		fixture += " | compose_draft"
	}
	if row.RunCtx.PendingFeedback != nil {
		fixture += " | feedback_overlay"
	}
	if row.RunCtx.WorkflowID != "" {
		fixture += " | workflow=" + row.RunCtx.WorkflowID
	}

	trim := []string{
		catalogfixture.StockAgentPromptTrimRel("coordinator-core.md"),
		catalogfixture.StockAgentPromptTrimRel("coordinator-mode-"+modeRefToPartial(row.ModeRefs)+".md"),
		filepath.ToSlash(filepath.Join("lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials", "coordinator-worker-chain-baseline.md")),
	}
	if row.SurfaceID == "implement_overlay_promote" {
		trim = append(trim, catalogfixture.StockAgentPromptTrimRel("coordinator-mode-implement-overlay-promote.md"))
	}
	if row.SurfaceID == "plan_stub" || row.SurfaceID == "plan_research" {
		trim = []string{
			catalogfixture.StockAgentPromptTrimRel("coordinator-core.md"),
			catalogfixture.StockAgentPromptTrimRel("coordinator-mode-"+modeRefToPartial(row.ModeRefs)+".md"),
		}
	}

	return promptBudgetEntry{
		Measures:  "Tripartite compile for fixture " + row.Name + " (ContextDietMatrix in coordinator_tripartite_fixtures.go).",
		Fixture:   fixture,
		TrimPaths: trim,
		BumpNote:  "Shared partials (worker-chain-baseline, coordinator-core) affect many fixtures — prefer trim; bump only when policy text is intentionally larger.",
	}
}

func modeRefToPartial(modeRefs []string) string {
	if len(modeRefs) == 0 {
		return "implement-synthesis"
	}
	return modeRefs[0]
}
