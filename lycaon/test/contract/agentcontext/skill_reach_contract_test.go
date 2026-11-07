package contract

import (
	"fmt"
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

// renderSkillTexts renders a skill body and every bundled markdown resource the
// way a read delivers them: one template execution per text with the reader's
// surface vars, plus the empty configuration map production keeps for packs
// without settings.
func renderSkillTexts(t *testing.T, sk skills.Skill, vars map[string]any) string {
	t.Helper()
	withConfiguration := make(map[string]any, len(vars)+1)
	for name, value := range vars {
		withConfiguration[name] = value
	}
	withConfiguration["configuration"] = map[string]any{}
	render := func(text, label string) string {
		t.Helper()
		tpl, err := pongoplain.Compile(text)
		contractcheck.FailErr(t, "compile "+label, err)
		out, err := pongoplain.ExecuteDetached(tpl, withConfiguration)
		contractcheck.FailErr(t, "render "+label, err)
		return out
	}
	parts := []string{render(sk.Body, sk.Name)}
	for _, rel := range sk.MarkdownResources() {
		body, err := sk.ReadResource(rel)
		contractcheck.FailErr(t, "read "+sk.Name+"/"+rel, err)
		parts = append(parts, render(string(body), sk.Name+"/"+rel))
	}
	return strings.Join(parts, "\n")
}

// readerHeld turns a reachable tool list into the map UnreachableNames checks,
// adding command mutation reachability from the rendered surface vars so the
// map matches exactly what the reader's template facts claim.
func readerHeld(reachable []string, vars map[string]any) map[string]bool {
	held := make(map[string]bool, len(reachable)+1)
	for _, name := range reachable {
		held[name] = true
	}
	if mutates, ok := vars["profile_command_mutation"].(bool); ok && mutates {
		held[toolvocab.ReachCommandMutation] = true
	}
	return held
}

func checkSkillReach(t *testing.T, catalog *toolvocab.Catalog, sk skills.Skill, reader string, corpus string, held map[string]bool) {
	t.Helper()
	if names := toolvocab.UnreachableNames(catalog, corpus, held); len(names) > 0 {
		t.Errorf("skill %q as read by %s names %s, which that reader cannot reach:\n%s",
			sk.Name, reader, strings.Join(names, ", "), linesNaming(corpus, names))
	}
}

// Every stock skill names only tools and command programs its reader can reach
// (INV-SK-01): coordinator surfaces across root counts, and every worker
// profile across branch and git states. Retired names count: a skill must not
// teach what no longer exists.
func TestStockSkillsNameOnlyReachableToolsAndPrograms(t *testing.T) {
	catalog, profiles := promptNameCatalog(t)
	skillSet := stockSkillCorpus(t)

	for _, rootCount := range []int{0, 1, 2} {
		plans, err := surface.CompileToolPlans(rootCount)
		contractcheck.FailErr(t, "compile tool plans", err)
		for id, plan := range plans {
			// With no folder attached, only the surfaces that reach no worktree open.
			if rootCount == 0 && !slices.Contains(noFolderSurfaces, id) {
				continue
			}
			reachable := plan.AddressableNames()
			offered := make(map[string]bool, len(reachable))
			for _, name := range reachable {
				offered[name] = true
			}
			for _, sk := range skillSet {
				if sk.Project || !toolvocab.SkillToolsOffered(sk, offered) {
					continue
				}
				t.Run(fmt.Sprintf("coordinator/%s/%d-roots/%s", id, rootCount, sk.Name), func(t *testing.T) {
					vars, err := prompts.SkillSurfaceTemplateVars(prompts.SkillSurfaceVarsInput{
						ProfileID:        prompts.CoordinatorProfileID,
						SurfaceID:        id,
						Offered:          reachable,
						HasGit:           rootCount > 0,
						RootCount:        rootCount,
						DelegationBudget: spawn.DefaultWorkerToolBudget(),
					})
					contractcheck.FailErr(t, "build skill vars", err)
					corpus := renderSkillTexts(t, sk, vars)
					checkSkillReach(t, catalog, sk, fmt.Sprintf("surface %s at %d roots", id, rootCount), corpus, readerHeld(reachable, vars))
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
		profile := profiles[idx]
		reachable := make([]string, 0, len(profile.Tools))
		for name, enabled := range profile.Tools {
			if enabled {
				reachable = append(reachable, name)
			}
		}
		offered := make(map[string]bool, len(reachable))
		for _, name := range reachable {
			offered[name] = true
		}
		for _, onBranch := range []bool{false, true} {
			for _, hasGit := range []bool{false, true} {
				for _, sk := range skillSet {
					if sk.Project || !toolvocab.SkillToolsOffered(sk, offered) {
						continue
					}
					t.Run(fmt.Sprintf("worker/%s/on-branch=%t/has-git=%t/%s", agent.ToolProfile, onBranch, hasGit, sk.Name), func(t *testing.T) {
						vars, err := prompts.SkillSurfaceTemplateVars(prompts.SkillSurfaceVarsInput{
							ProfileID:        agent.ToolProfile,
							Offered:          reachable,
							OnWorkerBranch:   onBranch,
							HasGit:           hasGit,
							RootCount:        1,
							DelegationBudget: spawn.DefaultWorkerToolBudget(),
						})
						contractcheck.FailErr(t, "build skill vars", err)
						corpus := renderSkillTexts(t, sk, vars)
						checkSkillReach(t, catalog, sk, fmt.Sprintf("profile %s (on branch: %t, git: %t)", agent.ToolProfile, onBranch, hasGit), corpus, readerHeld(reachable, vars))
					})
				}
			}
		}
	}
}
