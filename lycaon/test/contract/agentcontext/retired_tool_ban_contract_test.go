package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/laws/promptrender"
	"gopkg.in/yaml.v3"
)

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
	retired := retiredToolMention(t)
	root := contractcheck.RepoRoot(t)

	for _, rootCount := range []int{0, 1, 2} {
		plans, err := surface.CompileToolPlans(rootCount)
		contractcheck.FailErr(t, "compile tool plans", err)
		for id := range plans {
			if rootCount == 0 && !slices.Contains(noFolderSurfaces, id) {
				continue
			}
			t.Run(fmt.Sprintf("coordinator/%s/%d-roots", id, rootCount), func(t *testing.T) {
				checkBannedMention(t, retired,
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
			reader, err := prompts.DeclaredReader(eff, agent.ID)
			contractcheck.FailErr(t, "DeclaredReader "+agent.ID, err)
			corpus, err := prompts.RenderPersona(context.Background(), personaEngine, agent.ID, reader, nil)
			contractcheck.FailErr(t, "render persona", err)
			checkBannedMention(t, retired, "persona "+agent.ID, corpus)
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
					reader, err := prompts.NewReader(prompts.ReaderInput{
						ProfileID: profile.ID,
						Plan:      toolsurface.Compile(reachable, nil),
						Visible:   reachable,
						Profiles:  []sandbox.ToolProfile{profile},
						RootCount: 1,
						WebSearch: true,
					})
					contractcheck.FailErr(t, "new reader", err)
					data, err := prompts.Vars(reader, promptrender.KickFixture())
					contractcheck.FailErr(t, "prompts.Vars", err)
					corpus, err := kickEngine.RenderKick(context.Background(), binding.Render, data)
					contractcheck.FailErr(t, "render kick", err)
					checkBannedMention(t, retired, fmt.Sprintf("kick %s for %s", binding.Render, profile.ID), corpus)
				})
			}
			continue
		}
		if !strings.HasPrefix(binding.Render, "coordinator-") {
			continue
		}
		for id := range plans {
			t.Run("kick/"+binding.Render+"/"+id, func(t *testing.T) {
				data := promptrender.KickFixture()
				delete(data, "tools")
				prompts.MergeCoordinatorKickVarsForTools(data, coordinatorTestReader(t, id, 1))
				corpus, err := kickEngine.RenderKick(context.Background(), binding.Render, data)
				contractcheck.FailErr(t, "render kick", err)
				checkBannedMention(t, retired, fmt.Sprintf("kick %s on %s", binding.Render, id), corpus)
			})
		}
	}
}

// Phase guidance opens on the surface its workflow declares, so the retired
// ban checks each phase kick there too.
func TestRenderedPhaseKicksNeverNameFullyRetiredTools(t *testing.T) {
	retired := retiredToolMention(t)
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
			_, planKnown := plans[id]
			if inject.On != "phase.entered" || !known || !planKnown {
				continue
			}
			t.Run(filepath.Base(filepath.Dir(file))+"/"+inject.Selector.Phase, func(t *testing.T) {
				data := promptrender.KickFixture()
				prompts.MergeCoordinatorKickVarsForTools(data, coordinatorTestReader(t, id, 1))
				corpus, err := kickEngine.RenderKick(context.Background(), inject.Render, data)
				contractcheck.FailErr(t, "render kick", err)
				checkBannedMention(t, retired, fmt.Sprintf("phase kick %s on %s", inject.Render, id), corpus)
			})
		}
	}
}

func TestRenderedSkillsAndHintsNeverNameFullyRetiredTools(t *testing.T) {
	retired := retiredToolMention(t)
	forEachStockSkillRender(t, func(t *testing.T, _ skills.Skill, reader string, _ prompts.Reader, _ map[string]any, parts []promptrender.Text) {
		for _, part := range parts {
			checkBannedMention(t, retired, fmt.Sprintf("skill %s for %s", part.Label, reader), part.Text)
		}
	})
	hints, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint config", err)
	for code, entry := range hints.HintCodes {
		for _, text := range []string{entry.What, entry.Cause, entry.Why, entry.Fix, entry.Instead} {
			checkBannedMention(t, retired, "hint "+code, text)
		}
	}
}

// fullyRetiredTools derives the retired public tool names from the release
// retirement record that no shipped tool, operation or command program still
// answers to, in both spellings. UnreachableNames sees only registered names,
// so these need a direct check.
func fullyRetiredTools(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "schemas", "agent-public-retirements.json"))
	contractcheck.FailErr(t, "read retirement record", err)
	var record struct {
		Vocabularies struct {
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"vocabularies"`
	}
	contractcheck.FailErr(t, "decode retirement record", json.Unmarshal(raw, &record))
	language, err := toolcontract.ReadOnlyLanguageV1()
	contractcheck.FailErr(t, "load command language", err)
	programs := map[string]bool{}
	for _, name := range language.Language.RequiredCommands {
		programs[name] = true
	}
	catalog, _ := promptNameCatalog(t)
	var out []string
	for name := range record.Vocabularies.Tools {
		hyphen := strings.ReplaceAll(name, "_", "-")
		if len(toolvocab.ToolsIn(catalog, "`"+name+"`")) > 0 || programs[name] || programs[hyphen] {
			continue
		}
		out = append(out, name, hyphen)
	}
	if len(out) == 0 {
		t.Fatal("the retirement record yields no fully retired tool")
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func retiredToolMention(t *testing.T) *regexp.Regexp {
	t.Helper()
	return backtickedNameMention(fullyRetiredTools(t))
}
