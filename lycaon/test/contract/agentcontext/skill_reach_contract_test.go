package contract

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// stockSkillCorpus loads the stock pack skills with their bundled resources.
func stockSkillCorpus(t *testing.T) []skills.Skill {
	t.Helper()
	loaded, diags := extpacks.LoadEffectiveSkills(contractcheck.StockCatalog(t))
	if len(diags) > 0 {
		messages := make([]string, 0, len(diags))
		for _, diag := range diags {
			messages = append(messages, diag.UnitID+": "+diag.Message)
		}
		t.Fatalf("stock skills carry diagnostics:\n%s", strings.Join(messages, "\n"))
	}
	if len(loaded) == 0 {
		t.Fatal("no stock skills loaded")
	}
	return loaded
}

// renderedText is one rendered skill text with its label for diagnostics.
type renderedText struct {
	label    string
	text     string
	resource bool
}

// renderSkillParts renders a skill body and every bundled markdown resource the
// way a read delivers them: one template execution per text with the reader's
// surface vars, plus the empty configuration map production keeps for packs
// without settings.
func renderSkillParts(t *testing.T, sk skills.Skill, vars map[string]any) []renderedText {
	t.Helper()
	withConfiguration := make(map[string]any, len(vars)+1)
	for name, value := range vars {
		withConfiguration[name] = value
	}
	withConfiguration["configuration"] = map[string]any{}
	render := func(text, label string, resource bool) renderedText {
		t.Helper()
		tpl, err := pongoplain.Compile(text)
		contractcheck.FailErr(t, "compile "+label, err)
		out, err := pongoplain.ExecuteDetached(tpl, withConfiguration)
		contractcheck.FailErr(t, "render "+label, err)
		return renderedText{label: label, text: out, resource: resource}
	}
	parts := []renderedText{render(sk.Body, sk.Name, false)}
	for _, rel := range sk.MarkdownResources() {
		body, err := sk.ReadResource(rel)
		contractcheck.FailErr(t, "read "+sk.Name+"/"+rel, err)
		parts = append(parts, render(string(body), sk.Name+"/"+rel, true))
	}
	return parts
}

func checkSkillReach(t *testing.T, catalog *toolvocab.Catalog, sk skills.Skill, reader string, corpus string, held map[string]bool) {
	t.Helper()
	if names := toolvocab.UnreachableNames(catalog, corpus, held); len(names) > 0 {
		t.Errorf("skill %q as read by %s names %s, which that reader cannot reach:\n%s",
			sk.Name, reader, strings.Join(names, ", "), linesNaming(corpus, names))
	}
}

// forEachStockSkillRender walks every reader that can select a stock skill —
// coordinator surfaces across root counts, and every worker profile across
// branch and git states — renders each eligible skill for that reader, and
// calls check with the rendered parts.
func forEachStockSkillRender(t *testing.T, check func(t *testing.T, sk skills.Skill, readerLabel string, reader prompts.Reader, vars map[string]any, parts []renderedText)) {
	t.Helper()
	skillSet := stockSkillCorpus(t)
	_, profiles := promptNameCatalog(t)

	for _, rootCount := range []int{0, 1, 2} {
		plans, err := surface.CompileToolPlans(rootCount)
		contractcheck.FailErr(t, "compile tool plans", err)
		for id, plan := range plans {
			// With no folder attached, only the surfaces that reach no worktree open.
			if rootCount == 0 && !slices.Contains(noFolderSurfaces, id) {
				continue
			}
			loaded := make(map[string]bool, len(plan.AddressableNames()))
			for _, name := range plan.AddressableNames() {
				loaded[name] = true
			}
			reader, err := prompts.NewReader(prompts.ReaderInput{
				ProfileID: prompts.CoordinatorProfileID,
				SurfaceID: id,
				Profiles:  profiles,
				Plan:      plan,
				Loaded:    loaded,
				RootCount: rootCount,
				HasGit:    rootCount > 0,
				WebSearch: true,
			})
			contractcheck.FailErr(t, "build coordinator skill reader", err)
			held := reader.Held()
			for _, sk := range skillSet {
				if sk.Project || !toolvocab.SkillToolsOffered(sk, held) {
					continue
				}
				t.Run(fmt.Sprintf("coordinator/%s/%d-roots/%s", id, rootCount, sk.Name), func(t *testing.T) {
					vars := prompts.SkillVars(reader, spawn.DefaultWorkerToolBudget())
					check(t, sk, fmt.Sprintf("surface %s at %d roots", id, rootCount), reader, vars, renderSkillParts(t, sk, vars))
				})
			}
		}
	}

	eff := contractcheck.StockCatalog(t)
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	contractcheck.FailErr(t, "load agents", err)
	seenProfiles := map[string]bool{}
	for _, agent := range agents {
		if agent.ID == "coordinator" || seenProfiles[agent.ToolProfile] {
			continue
		}
		idx := slices.IndexFunc(profiles, func(p sandbox.ToolProfile) bool { return p.ID == agent.ToolProfile })
		if idx < 0 {
			continue
		}
		seenProfiles[agent.ToolProfile] = true
		for _, onBranch := range []bool{false, true} {
			for _, hasGit := range []bool{false, true} {
				reader, err := prompts.NewReader(prompts.ReaderInput{
					ProfileID:      agent.ToolProfile,
					Profiles:       profiles,
					OnWorkerBranch: onBranch,
					HasGit:         hasGit,
					RootCount:      1,
					WebSearch:      true,
				})
				contractcheck.FailErr(t, "build worker skill reader", err)
				held := reader.Held()
				for _, sk := range skillSet {
					if sk.Project || !toolvocab.SkillToolsOffered(sk, held) {
						continue
					}
					t.Run(fmt.Sprintf("worker/%s/on-branch=%t/has-git=%t/%s", agent.ToolProfile, onBranch, hasGit, sk.Name), func(t *testing.T) {
						vars := prompts.SkillVars(reader, spawn.DefaultWorkerToolBudget())
						check(t, sk, fmt.Sprintf("profile %s (on branch: %t, git: %t)", agent.ToolProfile, onBranch, hasGit), reader, vars, renderSkillParts(t, sk, vars))
					})
				}
			}
		}
	}
}

// Every stock skill names only tools and command programs its reader can reach
// (INV-SK-01): coordinator surfaces across root counts, and every worker
// profile across branch and git states. Retired names count: a skill must not
// teach what no longer exists.
func TestStockSkillsNameOnlyReachableToolsAndPrograms(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	forEachStockSkillRender(t, func(t *testing.T, sk skills.Skill, readerLabel string, reader prompts.Reader, _ map[string]any, parts []renderedText) {
		corpus := make([]string, len(parts))
		for i, part := range parts {
			corpus[i] = part.text
		}
		checkSkillReach(t, catalog, sk, readerLabel, strings.Join(corpus, "\n"), reader.Held())
	})
}

// Every rendered skill text fits its byte budget and is never empty
// (INV-SK-08): a guard that hides everything would leave the reader nothing,
// and a body that only fits unrendered would still ride every model call of
// the turn over budget.
func TestStockPackSkillsRenderedBudgets(t *testing.T) {
	forEachStockSkillRender(t, func(t *testing.T, sk skills.Skill, reader string, _ prompts.Reader, _ map[string]any, parts []renderedText) {
		for _, part := range parts {
			budget := skills.BundledBodyBudget
			kind := "body"
			if part.resource {
				budget = skills.BundledResourceBudget
				kind = "resource " + part.label
			}
			if len(part.text) > budget {
				t.Errorf("skill %q %s as read by %s renders %d bytes, over the %d-byte budget",
					sk.Name, kind, reader, len(part.text), budget)
			}
			if strings.TrimSpace(part.text) == "" {
				t.Errorf("skill %q %s as read by %s renders empty", sk.Name, kind, reader)
			}
		}
	})
}

var (
	emptyListItemRe      = regexp.MustCompile(`^\s*(\d+\.|[-*])\s*$`)
	spaceBeforePunctRe   = regexp.MustCompile(` [,;]`)
	spaceBeforeDotRe     = regexp.MustCompile(` \.`)
	inlineCodeSpanRe     = regexp.MustCompile("`[^`]*`")
	tableSeparatorCellRe = regexp.MustCompile(`^:?-+:?$`)
)

// prosePunctuationDrift reports space before punctuation in rendered prose.
// A space before a dot introducing an uppercase token is a dotted name such
// as .NET, not drift.
func prosePunctuationDrift(prose string) bool {
	if spaceBeforePunctRe.MatchString(prose) {
		return true
	}
	for _, loc := range spaceBeforeDotRe.FindAllStringIndex(prose, -1) {
		if loc[1] < len(prose) && prose[loc[1]] >= 'A' && prose[loc[1]] <= 'Z' {
			continue
		}
		return true
	}
	return false
}

// Every rendered skill body and reference keeps its prose structurally intact
// (INV-SK-09): template conditionals must never hollow out a numbered step,
// leave an empty backtick span, put space before punctuation, or blank a
// markdown table cell on any reader state.
func TestStockSkillsRenderedProseStructure(t *testing.T) {
	forEachStockSkillRender(t, func(t *testing.T, sk skills.Skill, reader string, _ prompts.Reader, _ map[string]any, parts []renderedText) {
		for _, part := range parts {
			checkRenderedProseStructure(t, sk.Name, part, reader)
		}
	})
}

func checkRenderedProseStructure(t *testing.T, skill string, part renderedText, reader string) {
	t.Helper()
	inFence := false
	for i, line := range strings.Split(part.text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		where := fmt.Sprintf("skill %q %s as read by %s, line %d", skill, part.label, reader, i+1)
		if emptyListItemRe.MatchString(line) {
			t.Errorf("%s: renders an empty list item %q", where, trimmed)
		}
		if strings.Contains(line, "``") {
			t.Errorf("%s: renders an empty backtick span: %q", where, trimmed)
		}
		isTableRow := strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
		if !isTableRow {
			// Inline code spans carry literal command syntax (./..., trailing
			// dot arguments), so punctuation there is not prose drift. Replace
			// each span with a letter: a space would fuse the span's trailing
			// punctuation into fake drift, and the letter keeps real drift
			// around the span visible.
			prose := inlineCodeSpanRe.ReplaceAllString(line, "x")
			if prosePunctuationDrift(prose) {
				t.Errorf("%s: renders space before punctuation: %q", where, trimmed)
			}
			continue
		}
		if cells := strings.Split(trimmed, "|"); blankedTableCells(cells) {
			t.Errorf("%s: renders a blanked table cell: %q", where, trimmed)
		}
	}
	for _, drift := range prompts.RenderedProseStructureDrift(part.text) {
		t.Errorf("skill %q %s as read by %s: %s", skill, part.label, reader, drift)
	}
}

// blankedTableCells reports whether a split table row carries a whitespace-only
// cell. Separator rows (| --- |) are not prose and never count.
func blankedTableCells(cells []string) bool {
	interior := cells[1 : len(cells)-1]
	separator := true
	for _, cell := range interior {
		if !tableSeparatorCellRe.MatchString(strings.TrimSpace(cell)) {
			separator = false
			break
		}
	}
	if separator {
		return false
	}
	for _, cell := range interior {
		if strings.TrimSpace(cell) == "" {
			return true
		}
	}
	return false
}

// TestSurveyRepoAliasAdmittedAtRenderLevel pins the PC09 alias end to end
// through the render-level reach gate: rendered copy may spell the repo-map
// form as `survey-repo`, the gate resolves it through the survey_repo form
// tool, and the alias stays gated on command and on survey_repo exactly like
// the canonical spelling.
func TestSurveyRepoAliasAdmittedAtRenderLevel(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	corpus := "Map the tree with `survey-repo` before reading files.\n"
	held := map[string]bool{"command": true, "survey_repo": true}
	if names := toolvocab.UnreachableNames(catalog, corpus, held); len(names) > 0 {
		t.Fatalf("survey-repo alias flagged unreachable for a command+survey_repo reader: %v", names)
	}
	for _, incomplete := range []map[string]bool{
		{"command": true},
		{"survey_repo": true},
	} {
		if names := toolvocab.UnreachableNames(catalog, corpus, incomplete); !slices.Contains(names, "survey-repo") {
			t.Fatalf("survey-repo must stay gated on command and survey_repo; held %v admitted it (unreachable = %v)", incomplete, names)
		}
	}
}
