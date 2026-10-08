package contract

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// promptBudgetEntry documents one measured prompt for failure digests and
// names the sources a change touches when it grows the prompt.
type promptBudgetEntry struct {
	Measures  string
	Fixture   string
	TrimPaths []string
	Advice    string
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
		"worker_personas":           workerPersonas,
		"coordinator_tripartite":    tripartite,
		"coordinator_injects":       injects,
		"agent_templates":           agentTemplates,
		"kicks":                     kicks,
		"worker_tool_surfaces":      toolSurfaceBudgetEntries(reg.LycaonRoot, reg.ToolProfileIDs()),
		"coordinator_tool_surfaces": coordinatorSurfaceBudgetEntries(reg.LycaonRoot, reg.CoordinatorSurfaces),
		"units":                     unitBudgetEntries(reg.LycaonRoot, reg.Units),
	}
}

func injectBudgetEntry(lycaonRoot string, spec PromptBudgetInjectSpec) promptBudgetEntry {
	return promptBudgetEntry{
		Measures:  fmt.Sprintf("Coordinator inject %q (%s).", spec.ID, spec.Template),
		Fixture:   spec.Fixture,
		TrimPaths: promptTemplateTrimPaths(lycaonRoot, spec.Template, spec.ExtraTrim...),
		Advice:    spec.Advice,
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
		Advice:    "Keep worker prompts focused; do not repeat coordinator policy in personas or playbooks.",
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
		catalogfixture.StockAgentPromptTrimRel("coordinator-mode-" + modeRefToPartial(row.ModeRefs) + ".md"),
		filepath.ToSlash(filepath.Join("lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials", "coordinator-worker-chain-baseline.md")),
	}
	if row.SurfaceID == "implement_overlay_promote" {
		trim = append(trim, catalogfixture.StockAgentPromptTrimRel("coordinator-mode-implement-overlay-promote.md"))
	}
	if row.SurfaceID == "plan_stub" || row.SurfaceID == "plan_research" {
		trim = []string{
			catalogfixture.StockAgentPromptTrimRel("coordinator-core.md"),
			catalogfixture.StockAgentPromptTrimRel("coordinator-mode-" + modeRefToPartial(row.ModeRefs) + ".md"),
		}
	}

	return promptBudgetEntry{
		Measures:  "Tripartite compile for fixture " + row.Name + " (ContextDietMatrix in coordinator_tripartite_fixtures.go).",
		Fixture:   fixture,
		TrimPaths: trim,
		Advice:    "Shared partials (worker-chain-baseline, coordinator-core) reach many fixtures; shorten them before mode-specific copy.",
	}
}

func modeRefToPartial(modeRefs []string) string {
	if len(modeRefs) == 0 {
		return "implement-synthesis"
	}
	return modeRefs[0]
}

// toolSurfaceBudgetEntries documents per-profile tool surfaces for failure digests.
func toolSurfaceBudgetEntries(lycaonRoot string, ids []string) map[string]promptBudgetEntry {
	sort.Strings(ids)
	out := make(map[string]promptBudgetEntry, len(ids))
	for _, id := range ids {
		if id == "coordinator" {
			continue
		}
		out[id] = promptBudgetEntry{
			Measures: "Wire-facing LLM tool definitions (name + description + parameters JSON) of profile " + id + "'s sticky tools.",
			Fixture:  "executor.List(ToolFilter{ProfileID: " + id + "})",
			TrimPaths: []string{
				checkoutPath(lycaonRoot, filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas")),
				checkoutPath(lycaonRoot, filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "profiles", id+".yaml")),
			},
			Advice: "Change cold tools from sticky to true in the profile so their schemas load on demand through request_tools.",
		}
	}
	return out
}

// coordinatorSurfaceBudgetEntries documents each coordinator surface's tools.
func coordinatorSurfaceBudgetEntries(lycaonRoot string, ids []string) map[string]promptBudgetEntry {
	out := make(map[string]promptBudgetEntry, len(ids))
	for _, id := range ids {
		out[id] = promptBudgetEntry{
			Measures: "Wire-facing tool definitions of coordinator surface " + id + "'s immediate tools, trimmed for the coordinator.",
			Fixture:  "surface.CompileToolPlans(1)[" + id + "].ImmediateNames()",
			TrimPaths: []string{
				checkoutPath(lycaonRoot, filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas")),
				checkoutPath(lycaonRoot, filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "host", "coordinator-surfaces.yaml")),
			},
			Advice: "Move cold tools from the floor to the surface's loadable list so their schemas load on demand through request_tools.",
		}
	}
	return out
}

var pongoIncludeRE = regexp.MustCompile(`\{%\s*include\s+"([^"]+)"`)

func promptTemplateTrimPaths(lycaonRoot, rel string, extra ...string) []string {
	rel = strings.TrimSpace(strings.TrimPrefix(rel, "/"))
	if rel == "" {
		return append([]string(nil), extra...)
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = checkoutPath(lycaonRoot, strings.TrimSpace(p))
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	switch {
	case strings.HasPrefix(rel, "kicks/"), strings.HasPrefix(rel, "inject/"), strings.HasPrefix(rel, "guidance/"):
		stem := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(rel, "kicks/"), "inject/"), "guidance/")
		if path, err := catalogfixture.StockGuidancePath(stem); err == nil {
			add(path.String())
		}
	case strings.HasPrefix(rel, "agents/"):
		add(catalogfixture.StockAgentPromptTrimRel(strings.TrimPrefix(rel, "agents/")))
	default:
		add(catalogfixture.StockAgentPromptTrimRel(rel))
		abs, err := catalogfixture.StockAgentPromptPath(rel)
		if err == nil {
			raw, readErr := abs.Read()
			if readErr == nil {
				for _, m := range pongoIncludeRE.FindAllStringSubmatch(string(raw), -1) {
					if len(m) < 2 {
						continue
					}
					add(catalogfixture.StockAgentPromptTrimRel(m[1]))
				}
			}
		}
	}
	for _, p := range extra {
		add(p)
	}
	sort.Strings(out)
	return out
}

// checkoutPath renders a pack source path as its location in the checkout. A
// bundled Source.String() is relative to the embedded config filesystem, so it
// sits under lycaon/config rather than lycaon.
func checkoutPath(lycaonRoot, p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Join("lycaon", "config", filepath.FromSlash(p)))
	}
	rel, err := filepath.Rel(lycaonRoot, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(filepath.Join("lycaon", rel))
}

func kickTemplateIDs() ([]string, error) {
	packs, err := extpacks.DiscoverStock()
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var ids []string
	for _, dir := range extpacks.KindDirs(packs, "guidance") {
		entries, err := dir.List()
		if err != nil {
			return nil, err
		}
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
				continue
			}
			id := strings.TrimSuffix(ent.Name(), ".md")
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// unitBudgetEntries documents each stock instruction unit by its template.
func unitBudgetEntries(lycaonRoot string, units []promptunit.Unit) map[string]promptBudgetEntry {
	out := make(map[string]promptBudgetEntry, len(units))
	for _, unit := range units {
		var trim []string
		matches, _ := filepath.Glob(filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "*", "shared", "units", unit.ID+".md"))
		for _, match := range matches {
			trim = append(trim, checkoutPath(lycaonRoot, match))
		}
		out[unit.ID] = promptBudgetEntry{
			Measures:  "Instruction unit " + unit.ID + " (" + string(unit.Slot) + " slot), rendered with coordinator template variables",
			Fixture:   "RenderUnit(" + unit.Ref + ")",
			TrimPaths: trim,
		}
	}
	return out
}
