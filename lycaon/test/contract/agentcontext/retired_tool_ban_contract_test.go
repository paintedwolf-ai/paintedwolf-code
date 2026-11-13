package contract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// fullyRetiredTools are tool names the host no longer registers anywhere: no
// native tool, no command operation, and no command form survives under the
// name or a hyphenated spelling. The catalog cannot see them — toolvocab
// filters named calls to registered tools and operations — so UnreachableNames
// stays silent when copy reintroduces one, on every reader profile. This test
// is the only gate for the class (INV-5): agent-facing copy must never
// instruct calling a retired tool.
var fullyRetiredTools = []string{
	"git_read", "git_write", "web_fetch", "archive_extract", "archive_create", "file_info", "jq_query",
	"git-read", "git-write", "web-fetch", "archive-extract", "archive-create", "file-info", "jq-query",
}

// backtickedNameMention matches a backtick-opened name in a call-like shape:
// closed span, JSON-call parenthesis, or a following flag. A longer name that
// merely starts with one of these (for example `editor_file` after `edit`)
// does not match, because the next character is not a boundary.
func backtickedNameMention(names []string) *regexp.Regexp {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, regexp.QuoteMeta(name))
	}
	return regexp.MustCompile("`(?:" + strings.Join(quoted, "|") + ")(?:`|\\(|\\s|$)")
}

var retiredToolMention = backtickedNameMention(fullyRetiredTools)

// mutationSubcommands are the command forms that mutate files. A profile that
// holds command without mutation cannot reach any of them, so rendered copy
// for such a reader must never name one: naming it teaches a call the reader
// cannot make. The skill matrix pins the same class through UnreachableNames;
// this test pins it at prompt level.
var mutationSubcommands = []string{"edit", "replace-lines", "replace_lines", "code-rewrite", "code_rewrite", "jq-edit", "jq_edit"}

var mutationSubcommandMention = backtickedNameMention(mutationSubcommands)

func checkBannedMention(t *testing.T, mention *regexp.Regexp, label, corpus string) {
	t.Helper()
	loc := mention.FindStringIndex(corpus)
	if loc == nil {
		return
	}
	line := 1 + strings.Count(corpus[:loc[0]], "\n")
	lineEnd := strings.IndexByte(corpus[loc[0]:], '\n')
	shown := corpus[loc[0]:]
	if lineEnd >= 0 {
		shown = corpus[loc[0] : loc[0]+lineEnd]
	}
	t.Errorf("%s names a banned call at line %d: %q", label, line, shown)
}

// TestRenderedStockCopyNeverNamesFullyRetiredTools walks the stock render
// matrix — coordinator surfaces, worker personas, and kicks — and fails when
// rendered copy presents a fully retired tool name as a call.
func TestRenderedStockCopyNeverNamesFullyRetiredTools(t *testing.T) {
	root := contractcheck.RepoRoot(t)

	for _, rootCount := range []int{0, 1, 2} {
		plans, err := surface.CompileToolPlans(rootCount)
		contractcheck.FailErr(t, "compile tool plans", err)
		for id := range plans {
			if rootCount == 0 && !slices.Contains(noFolderSurfaces, id) {
				continue
			}
			t.Run(fmt.Sprintf("coordinator/%s/%d-roots", id, rootCount), func(t *testing.T) {
				checkBannedMention(t, retiredToolMention,
					fmt.Sprintf("coordinator surface %s at %d roots", id, rootCount),
					renderCoordinatorTripartiteAtRootCount(t, root, rootCount, id))
			})
		}
	}

	eff := contractcheck.StockCatalog(t)
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	contractcheck.FailErr(t, "load agents", err)
	personaEngine := contractPersonaEngine(t)
	for _, agent := range agents {
		if agent.ID == "coordinator" {
			continue
		}
		t.Run("persona/"+agent.ID, func(t *testing.T) {
			corpus, err := prompts.RenderPersona(context.Background(), personaEngine, agent.ID, nil)
			contractcheck.FailErr(t, "render persona", err)
			checkBannedMention(t, retiredToolMention, "persona "+agent.ID, corpus)
		})
	}

	kickEngine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile tool plans", err)
	_, profiles := promptNameCatalog(t)
	for _, binding := range catalogfixture.LoadInformBindings(t).AllInform() {
		if binding == nil || !binding.IsInform() {
			continue
		}
		if strings.HasPrefix(binding.Render, "worker-") {
			for _, profile := range profiles {
				if !profile.Tools["complete_leg"] {
					continue
				}
				t.Run("kick/"+binding.Render+"/"+profile.ID, func(t *testing.T) {
					var reachable []string
					for name, enabled := range profile.Tools {
						if enabled {
							reachable = append(reachable, name)
						}
					}
					data := kickRenderFixture()
					for key, value := range prompts.VisibleToolsNativePartialVars(reachable) {
						data[key] = value
					}
					corpus, err := kickEngine.RenderKick(context.Background(), binding.Render, data)
					contractcheck.FailErr(t, "render kick", err)
					checkBannedMention(t, retiredToolMention, fmt.Sprintf("kick %s for %s", binding.Render, profile.ID), corpus)
				})
			}
			continue
		}
		if !strings.HasPrefix(binding.Render, "coordinator-") {
			continue
		}
		for id, plan := range plans {
			t.Run("kick/"+binding.Render+"/"+id, func(t *testing.T) {
				data := kickRenderFixture()
				delete(data, "tools")
				contractcheck.FailErr(t, "merge kick vars", prompts.MergeCoordinatorKickVarsForTools(data, plan.AddressableNames()))
				corpus, err := kickEngine.RenderKick(context.Background(), binding.Render, data)
				contractcheck.FailErr(t, "render kick", err)
				checkBannedMention(t, retiredToolMention, fmt.Sprintf("kick %s on %s", binding.Render, id), corpus)
			})
		}
	}
}

// TestReadOnlyCommandProfilesNeverNameMutationSubcommands renders every
// persona and worker kick whose profile holds command without mutation and
// fails when the render names an edit subcommand. The read-only branch of the
// command surface and the guarded secret-reference copy already keep the
// names out; this test pins that discipline so a regression cannot reintroduce
// the call on a reader that cannot make it.
func TestReadOnlyCommandProfilesNeverNameMutationSubcommands(t *testing.T) {
	_, profiles := promptNameCatalog(t)
	mutation, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	contractcheck.FailErr(t, "build mutation effects", err)
	readonly := map[string]bool{}
	for _, profile := range profiles {
		if profile.Tools["command"] && profile.CommandEffects != 0 && !profile.CommandEffects.Allows(mutation) {
			readonly[profile.ID] = true
		}
	}
	if len(readonly) == 0 {
		t.Fatal("no read-only command profile found — the profile set or this test's detection is broken")
	}

	eff := contractcheck.StockCatalog(t)
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	contractcheck.FailErr(t, "load agents", err)
	personaEngine := contractPersonaEngine(t)
	checked := 0
	for _, agent := range agents {
		if agent.ID == "coordinator" || !readonly[agent.ToolProfile] {
			continue
		}
		checked++
		t.Run("persona/"+agent.ID, func(t *testing.T) {
			corpus, err := prompts.RenderPersona(context.Background(), personaEngine, agent.ID, nil)
			contractcheck.FailErr(t, "render persona", err)
			checkBannedMention(t, mutationSubcommandMention,
				fmt.Sprintf("read-only profile %s (agent %s)", agent.ToolProfile, agent.ID), corpus)
		})
	}
	if checked == 0 {
		t.Fatal("no agent renders on a read-only command profile — the pinned class has no corpus")
	}

	kickEngine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, binding := range catalogfixture.LoadInformBindings(t).AllInform() {
		if binding == nil || !binding.IsInform() || !strings.HasPrefix(binding.Render, "worker-") {
			continue
		}
		for _, profile := range profiles {
			if !readonly[profile.ID] || !profile.Tools["complete_leg"] {
				continue
			}
			t.Run("kick/"+binding.Render+"/"+profile.ID, func(t *testing.T) {
				var reachable []string
				for name, enabled := range profile.Tools {
					if enabled {
						reachable = append(reachable, name)
					}
				}
				data := kickRenderFixture()
				for key, value := range prompts.VisibleToolsNativePartialVars(reachable) {
					data[key] = value
				}
				corpus, err := kickEngine.RenderKick(context.Background(), binding.Render, data)
				contractcheck.FailErr(t, "render kick", err)
				checkBannedMention(t, mutationSubcommandMention,
					fmt.Sprintf("read-only profile %s (kick %s)", profile.ID, binding.Render), corpus)
			})
		}
	}
}

// Phase guidance opens on the surface its workflow declares, so the retired
// ban checks each phase kick there too.
func TestRenderedPhaseKicksNeverNameFullyRetiredTools(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "lycaon/config/packs/painted-wolf/*/workflows/*/workflow.yaml"))
	contractcheck.FailErr(t, "list workflows", err)
	if len(files) == 0 {
		t.Fatal("no stock workflows")
	}
	kickEngine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile tool plans", err)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		contractcheck.FailErr(t, "read workflow", err)
		var workflow workflowPhaseBindings
		contractcheck.FailErr(t, "decode workflow", yaml.Unmarshal(raw, &workflow))
		surfaces := map[string]string{}
		for _, phase := range workflow.Phases {
			surfaces[phase.ID] = phase.Surface
		}
		for _, inject := range workflow.Injects {
			id, known := surfaces[inject.Selector.Phase]
			plan, planKnown := plans[id]
			if inject.On != "phase.entered" || !known || !planKnown {
				continue
			}
			t.Run(filepath.Base(filepath.Dir(file))+"/"+inject.Selector.Phase, func(t *testing.T) {
				data := kickRenderFixture()
				contractcheck.FailErr(t, "merge kick vars", prompts.MergeCoordinatorKickVarsForTools(data, plan.AddressableNames()))
				corpus, err := kickEngine.RenderKick(context.Background(), inject.Render, data)
				contractcheck.FailErr(t, "render kick", err)
				checkBannedMention(t, retiredToolMention, fmt.Sprintf("phase kick %s on %s", inject.Render, id), corpus)
			})
		}
	}
}
