package promptrender

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// Family is the prompt-render rule table.
var Family = invariants.Family{Name: "prompt-render", Invariants: append([]invariants.Invariant{
	{Name: "agent-personas-render", Anchors: []invariants.Anchor{personaRenderer}, Check: agentPersonasRender},
	{Name: "agent-templates-render", Anchors: []invariants.Anchor{personaRenderer}, Check: agentTemplatesRender},
	{Name: "coordinator-kicks-render-non-empty", Anchors: []invariants.Anchor{kickRenderer}, Check: coordinatorKicksRenderNonEmpty},
	{Name: "read-only-profiles-never-name-mutation-subcommands", Anchors: []invariants.Anchor{personaRenderer, kickRenderer}, Check: readOnlyProfilesNeverNameMutations},
	{Name: "skills-fit-their-budgets", Anchors: []invariants.Anchor{invariants.Decl("internal/skills", "BundledBodyBudget")}, Check: skillsFitTheirBudgets},
	{Name: "skill-prose-stays-intact", Anchors: []invariants.Anchor{invariants.Func("internal/prompts", "RenderedProseStructureDrift")}, Check: skillProseStaysIntact},
	{Name: "templates-render-no-host-paths", Anchors: []invariants.Anchor{invariants.Dir("lycaon/config/packs/painted-wolf/platform/guidance")}, Exempt: []invariants.Exemption{
		{Key: "config/packs/painted-wolf/platform/guidance/curator/user.md: c.path", Reason: "the curator's evidence path is a display path, not a host root"},
	}, Check: templatesRenderNoHostPaths},
}, reachInvariants...)}

var (
	personaRenderer = invariants.Func("internal/prompts", "RenderPersona")
	kickRenderer    = invariants.Decl("internal/prompts", "FileTemplateEngine.RenderKick")
)

// stock is the stock catalog, read once per corpus.
func stock(r *invariants.Run) *Stock {
	return invariants.Part(r.Corpus, "stock prompt catalog", func() (*Stock, error) {
		s, err := LoadStock(context.Background())
		if err == nil {
			s.Site = r.Path("lycaon/" + protectedpath.PromptFilesDirName)
		}
		return s, err
	})
}

// skillRenders is every stock skill rendered for every reader, once per
// corpus.
func skillRenders(r *invariants.Run) []SkillRender {
	return invariants.Part(r.Corpus, "stock skill renders", func() ([]SkillRender, error) { return RenderStockSkills(stock(r)) })
}

// Every agent the persona contract declares renders its persona.
func agentPersonasRender(r *invariants.Run) {
	contract, err := prompts.LoadPersonaContractWithCatalog(stock(r).Catalog)
	r.Must(err)
	r.Expect(len(contract.Agents), "persona contract agents")
	engine := PersonaEngine()
	for _, id := range sortedKeys(contract.Agents) {
		reader, err := prompts.DeclaredReader(stock(r).Catalog, id)
		r.Must(err)
		if _, err := prompts.RenderPersona(context.Background(), engine, id, reader, nil); err != nil {
			r.Report("agent %s: %v", id, err)
		}
	}
}

// Every bundled agent's system prompt template renders, and the gate agents
// it references exist.
func agentTemplatesRender(r *invariants.Run) {
	reg := orchestration.NewMemoryAgentRegistry()
	r.Must(orchestration.LoadRequiredAgentRegistry(context.Background(), reg))
	if err := orchestration.ValidateGateAgents(reg); err != nil {
		r.Report("gate agent references: %v", err)
	}
	contract, err := prompts.LoadPersonaContractWithCatalog(stock(r).Catalog)
	r.Must(err)
	engine := PersonaEngine()
	for _, profile := range reg.List() {
		if profile.SystemPromptTemplate == "" {
			continue
		}
		if contract.IsWorkerAgent(profile.ID) {
			reader, err := prompts.DeclaredReader(stock(r).Catalog, profile.ID)
			r.Must(err)
			if _, err := prompts.RenderPersona(context.Background(), engine, profile.ID, reader, nil); err != nil {
				r.Report("agent %s persona: %v", profile.ID, err)
			}
			continue
		}
		if _, err := engine.Render(context.Background(), profile.SystemPromptTemplate, nil); err != nil {
			r.Report("agent %s template %s: %v", profile.ID, profile.SystemPromptTemplate, err)
		}
	}
}

// KickFixture is event data with every field a kick reads.
func KickFixture() map[string]any {
	return map[string]any{
		"batch_phase":          "integrate",
		"worker_digest":        "ENV report: 2 files changed, build green.",
		"topology_output":      "Recon pack: 3 risks surfaced.",
		"evidence_digest":      "## Evidence digest (curated)\n[grep#1] main.go:1 — package main\nselected/total: 1/1",
		"options_criterion":    "Prefer minimal long-term surface area.",
		"pending_overlay_jobs": []string{"job-a", "job-b"},
		"partial_worker_jobs":  []string{"job-partial"},
		"promoted_paths":       []string{"lycaon/internal/x.go", "lycaon/internal/y.go"},
		"completed_ago":        "2m ago",
		"scan_id":              "scan-1234",
		"scanner_id":           "fixture-scanner",
		"introduced_count":     1,
		"fixed_count":          1,
		"introduced":           []map[string]any{{"level": "high", "rule_id": "fixture-rule", "path": "main.go", "line": 4, "message": "fixture finding"}},
		"fixed":                []map[string]any{{"level": "high", "rule_id": "fixture-rule", "path": "fixed.go", "line": 8}},
		"batches":              6,
		"tools":                []string{"command"},
		"scan_status":          "complete",
		"scan_categories":      "sast, secret",
		"scan_findings_count":  7,
		"failed_leaves":        []string{"hitl_consulted:intake"},
		"gate_obligations": []map[string]any{{
			"id":      "hitl_consulted:intake",
			"purpose": "Plan intake is freeform must-consult.",
			"satisfy": []string{"Call ask_user with task-appropriate questions when no host card is pending."},
		}},
	}
}

// CoordinatorReader is the coordinator's reader on one surface.
func CoordinatorReader(stock *Stock, surfaceID string, roots int) (prompts.Reader, error) {
	plan, err := prompts.CompileSurfacePlan(surfaceID, roots)
	if err != nil {
		return prompts.Reader{}, err
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID, SurfaceID: surfaceID, Plan: plan,
		Profiles: stock.Profiles, RootCount: roots, WebSearch: true,
	})
}

// Every coordinator kick renders non-empty prose, with every event field set
// and with none.
func coordinatorKicksRenderNonEmpty(r *invariants.Run) {
	ids, err := KickTemplateIDs(stock(r).Catalog)
	r.Must(err)
	reader, err := CoordinatorReader(stock(r), "implement_investigate", 1)
	r.Must(err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	full, minimal := KickFixture(), map[string]any{}
	prompts.MergeCoordinatorKickVarsForTools(full, reader)
	prompts.MergeCoordinatorKickVarsForTools(minimal, reader)
	checked := 0
	for _, id := range ids {
		if !strings.HasPrefix(id, "coordinator-") {
			continue
		}
		checked++
		for label, data := range map[string]map[string]any{"every field": full, "no event fields": minimal} {
			out, err := engine.RenderKick(context.Background(), id, data)
			switch {
			case err != nil:
				r.Report("%s with %s: %v", id, label, err)
			case strings.TrimSpace(out) == "":
				r.Report("%s renders empty with %s", id, label)
			}
		}
	}
	r.Expect(checked, "coordinator kick templates")
}

// mutationMention matches a file-mutating command form named as a call.
var mutationMention = regexp.MustCompile("`(?:edit|replace-lines|replace_lines|code-rewrite|code_rewrite|jq-edit|jq_edit)(?:`|\\(|\\s|$)")

// A persona or worker kick read on a read-only command profile never names a
// command form that mutates files.
func readOnlyProfilesNeverNameMutations(r *invariants.Run) {
	mutation, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	r.Must(err)
	readonly := map[string]sandbox.ToolProfile{}
	for _, p := range stock(r).Profiles {
		if p.Tools["command"] && p.CommandEffects != 0 && !p.CommandEffects.Allows(mutation) {
			readonly[p.ID] = p
		}
	}
	r.Expect(len(readonly), "read-only command profiles")
	agents, err := agentdef.LoadEffectiveWithCatalog(stock(r).Catalog)
	r.Must(err)
	engine := PersonaEngine()
	personas := 0
	for _, agent := range agents {
		if _, ok := readonly[agent.ToolProfile]; !ok || agent.ID == "coordinator" {
			continue
		}
		personas++
		reader, err := prompts.DeclaredReader(stock(r).Catalog, agent.ID)
		r.Must(err)
		text, err := prompts.RenderPersona(context.Background(), engine, agent.ID, reader, nil)
		r.Must(err)
		reportMention(r, fmt.Sprintf("persona %s on read-only profile %s", agent.ID, agent.ToolProfile), text)
	}
	r.Expect(personas, "personas on read-only command profiles")
	ids, err := KickTemplateIDs(stock(r).Catalog)
	r.Must(err)
	kicks := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, id := range ids {
		if !strings.HasPrefix(id, "worker-") {
			continue
		}
		for _, pid := range sortedKeys(readonly) {
			profile := readonly[pid]
			if !profile.Tools["complete_leg"] {
				continue
			}
			var reachable []string
			for name, enabled := range profile.Tools {
				if enabled {
					reachable = append(reachable, name)
				}
			}
			reader, err := prompts.NewReader(prompts.ReaderInput{
				ProfileID: pid, Plan: toolsurface.Compile(reachable, nil), Visible: reachable,
				Profiles: []sandbox.ToolProfile{profile}, RootCount: 1, WebSearch: true,
			})
			r.Must(err)
			data, err := prompts.Vars(reader, KickFixture())
			r.Must(err)
			text, err := kicks.RenderKick(context.Background(), id, data)
			r.Must(err)
			reportMention(r, fmt.Sprintf("kick %s on read-only profile %s", id, pid), text)
		}
	}
}

func reportMention(r *invariants.Run, label, text string) {
	loc := mutationMention.FindStringIndex(text)
	if loc == nil {
		return
	}
	line := 1 + strings.Count(text[:loc[0]], "\n")
	shown, _, _ := strings.Cut(text[loc[0]:], "\n")
	r.Report("%s names a mutating command at line %d: %q", label, line, shown)
}

// Every rendered skill text fits its byte budget and is never empty: a guard
// that hides everything leaves the reader nothing, and a body that only fits
// unrendered still rides every model call of the turn over budget.
func skillsFitTheirBudgets(r *invariants.Run) {
	renders := skillRenders(r)
	r.Expect(len(renders), "stock skill renders")
	for _, sr := range renders {
		for _, part := range sr.Parts {
			budget, kind := skills.BundledBodyBudget, "body"
			if part.Resource {
				budget, kind = skills.BundledResourceBudget, "resource "+part.Label
			}
			if len(part.Text) > budget {
				r.Report("skill %s %s as read by %s renders %d bytes, over the %d-byte budget", sr.Skill.Name, kind, sr.Reader, len(part.Text), budget)
			}
			if strings.TrimSpace(part.Text) == "" {
				r.Report("skill %s %s as read by %s renders empty", sr.Skill.Name, kind, sr.Reader)
			}
		}
	}
}

// Template conditionals never hollow out a numbered step, leave an empty
// backtick span, put space before punctuation, or blank a table cell on any
// reader state.
func skillProseStaysIntact(r *invariants.Run) {
	renders := skillRenders(r)
	r.Expect(len(renders), "stock skill renders")
	for _, sr := range renders {
		for _, part := range sr.Parts {
			for _, drift := range ProseDrift(part.Text) {
				r.Report("skill %s %s as read by %s: %s", sr.Skill.Name, part.Label, sr.Reader, drift)
			}
		}
	}
}

var (
	hostPathVar      = regexp.MustCompile(`\b(?:project_dir|session_scratch_dir|worker_workspace_path|(?:r|c|root|new_primary|active_root)\.path|c\.old_path)\b`)
	templateOutput   = regexp.MustCompile(`\{\{([^}]*)\}\}`)
	coordinatorKicks = regexp.MustCompile(`^config/packs/painted-wolf/platform/guidance/coordinator-[^/]*\.md$`)
)

// No config template renders a host absolute path: workspace roots, scratch
// and worker workspaces are addressed by @label.
func templatesRenderNoHostPaths(r *invariants.Run) {
	for _, f := range r.Tree("lycaon/config", ".md") {
		rel := "config/" + f.Rel
		for _, m := range templateOutput.FindAllStringSubmatch(f.Text(), -1) {
			expr := strings.TrimSpace(m[1])
			hit := hostPathVar.FindString(expr)
			if hit == "" && expr == "path" && coordinatorKicks.MatchString(rel) {
				hit = expr
			}
			if hit != "" && !r.Exempt(rel+": "+hit) {
				r.Report("%s renders a host path: {{%s}}", rel, expr)
			}
		}
	}
}
