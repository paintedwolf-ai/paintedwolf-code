package contract

// Temporary SK05 probe: report every tool or command-form mention in stock
// skill bodies and references/*.md that some selecting agent profile cannot
// reach, or can reach only without command mutation. Deleted once the SK05
// migration lands; SK06 replaces it with the standing contract test.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var probeGuardedRegion = func() *strings.Replacer { return nil }()

func probeBlankGuards(text string) string {
	var b strings.Builder
	rest := text
	for {
		start := strings.Index(rest, "{% if")
		if start < 0 {
			b.WriteString(rest)
			return b.String()
		}
		end := strings.Index(rest[start:], "{% endif %}")
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(strings.Map(func(r rune) rune {
			if r == '\n' {
				return '\n'
			}
			return ' '
		}, rest[start : start+end+len("{% endif %}")]))
		rest = rest[start+end+len("{% endif %}"):]
	}
}

func TestProbeSkillMentionReachability(t *testing.T) {
	eff := contractcheck.StockCatalog(t)
	contractcheck.ActivateStockCatalog(t)
	catalog, profiles := promptNameCatalog(t)
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	contractcheck.FailErr(t, "load agents", err)
	loaded, _ := extpacks.LoadEffectiveSkills(extpacks.Active())
	if len(loaded) == 0 {
		t.Fatal("expected stock pack skills")
	}
	profileByID := map[string]sandbox.ToolProfile{}
	for _, p := range profiles {
		profileByID[p.ID] = p
	}
	language, err := toolcontract.ReadOnlyLanguageV1()
	contractcheck.FailErr(t, "load command language", err)
	formTool := map[string]string{}
	for _, disp := range language.Tools {
		if !toolcontract.CommandFormTool(disp.Tool) {
			continue
		}
		for _, form := range disp.CommandForms {
			formTool[form] = disp.Tool
		}
	}
	mutatingForms := map[string]bool{}
	for _, disp := range language.Tools {
		if disp.Tool != "command" {
			continue
		}
		for _, form := range disp.CommandForms {
			mutatingForms[form] = true
		}
	}
	mutation, _ := toolcontract.NewCommandEffects(toolcontract.CommandMutation)

	type violation struct{ agent, profile, kind, line string }
	byFile := map[string][]violation{}
	for _, sk := range loaded {
		if sk.Project {
			continue
		}
		var selectors []agentdef.Profile
		for _, agent := range agents {
			if agent.Skills.All || slices.Contains(agent.Skills.Names, sk.Name) {
				selectors = append(selectors, agent)
			}
		}
		if len(selectors) == 0 {
			continue
		}
		files := map[string]string{"SKILL.md": sk.Body}
		for _, rel := range sk.MarkdownResources() {
			body, err := sk.ReadResource(rel)
			if err != nil {
				t.Fatalf("%s: read %s: %v", sk.Name, rel, err)
			}
			files[rel] = string(body)
		}
		for rel, text := range files {
			blanked := probeBlankGuards(text)
			for i, line := range strings.Split(blanked, "\n") {
				for _, name := range toolvocab.ToolsIn(catalog, line) {
					for _, agent := range selectors {
						pid := strings.TrimSpace(agent.ToolProfile)
						if catalog.ProfileHolds(pid, name) {
							if mutatingForms[name] && formTool[name] == "command" {
								p := profileByID[pid]
								if !p.CommandEffects.Allows(mutation) {
									byFile[sk.Name+"/"+rel] = append(byFile[sk.Name+"/"+rel], violation{
										agent: agent.ID, profile: pid, kind: "mutation",
										line: fmt.Sprintf("%d: `%s` needs command mutation", i+1, name),
									})
								}
							}
							continue
						}
						program := catalog.ProfileHolds(pid, "command") &&
							(formTool[name] == "" || catalog.ProfileHolds(pid, formTool[name])) &&
							slices.ContainsFunc(language.Commands, func(spec toolcontract.CommandSpecification) bool {
								return spec.Name == name
							})
						if program {
							continue
						}
						byFile[sk.Name+"/"+rel] = append(byFile[sk.Name+"/"+rel], violation{
							agent: agent.ID, profile: pid, kind: "unreachable",
							line: fmt.Sprintf("%d: `%s` not held", i+1, name),
						})
					}
				}
			}
		}
	}
	keys := make([]string, 0, len(byFile))
	for k := range byFile {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		seen := map[string]bool{}
		var uniq []violation
		for _, v := range byFile[k] {
			key := v.profile + "|" + v.line
			if seen[key] {
				continue
			}
			seen[key] = true
			uniq = append(uniq, v)
		}
		t.Errorf("%s:\n%s", k, func() string {
			var b strings.Builder
			for _, v := range uniq {
				fmt.Fprintf(&b, "  [%s] %s: %s\n", v.kind, v.profile, v.line)
			}
			return b.String()
		}())
	}
}
