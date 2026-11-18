package contract

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func promptNameCatalog(t *testing.T) (*toolvocab.Catalog, []sandbox.ToolProfile) {
	t.Helper()
	eff := contractcheck.StockCatalog(t)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	contractcheck.FailErr(t, "load tool profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	contractcheck.FailErr(t, "load tool schemas", err)
	catalog, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	contractcheck.FailErr(t, "toolvocab.NewCatalog", err)
	return catalog, profiles
}

func unreachableNames(t *testing.T, catalog *toolvocab.Catalog, corpus string, reader prompts.Reader) []string {
	t.Helper()
	return toolvocab.UnreachableNames(catalog, corpus, reader.Held())
}

func linesNaming(corpus string, names []string) string {
	var out []string
	for _, line := range strings.Split(corpus, "\n") {
		for _, name := range names {
			handle := strings.HasSuffix(name, "#") && strings.Contains(line, name)
			if handle || strings.Contains(line, "`"+name+"`") || strings.Contains(line, "`"+name+"(") {
				out = append(out, line)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// A rendered prompt names a tool only when the surface it renders for can reach
// it. Retired names count: a prompt must not teach what no longer exists.
func TestRenderedCoordinatorPromptsNameOnlyReachableTools(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	states := map[string]surface.ImplementSessionState{
		"idle":              {},
		"workers in flight": {WorkersInFlight: 2},
		"pending overlays":  {PendingOverlayIDs: []string{"job-overlay-1"}, WorkersInFlight: 1},
	}
	for _, rootCount := range []int{0, 1, 2} {
		plans, err := surface.CompileToolPlans(rootCount)
		contractcheck.FailErr(t, "compile tool plans", err)
		for id := range plans {
			reader := coordinatorTestReader(t, id, rootCount)
			check := func(t *testing.T, corpus string) {
				t.Helper()
				if names := unreachableNames(t, catalog, corpus, reader); len(names) > 0 {
					t.Errorf("names %s, which this surface cannot reach:\n%s", strings.Join(names, ", "), linesNaming(corpus, names))
				}
			}
			// With no folder attached, only the surfaces that reach no worktree open.
			if rootCount > 0 || slices.Contains(noFolderSurfaces, id) {
				t.Run(fmt.Sprintf("%s/%d-roots", id, rootCount), func(t *testing.T) {
					check(t, renderCoordinatorTripartiteAtRootCount(t, rootCount, id))
				})
			}
			if rootCount != 1 {
				continue
			}
			for name, state := range states {
				t.Run(id+"/"+name, func(t *testing.T) {
					check(t, renderCoordinatorTripartiteForRunContext(t, api.CoordinatorRunContext{},
						&api.Session{Posture: api.SessionPostureBuild}, surface.HostLoopWakeSentinel,
						WorkerCompletionHistory("implementer"), id, state))
				})
			}
		}
	}
}

var noFolderSurfaces = []string{surfaceImplementRouting, "implement_investigate", surfaceImplementSynthesis}

// phaseKicks render only on their workflow phase, whose surface the phase declares.
var phaseKickPrefixes = []string{
	"coordinator-debate-", "coordinator-fanout-", "coordinator-phase-exit", "coordinator-plan-", "coordinator-recon-",
	"coordinator-review-loop-", "coordinator-security-",
}

// A wake can reach any surface, so it names only what that surface's plan reaches.
func TestRenderedCoordinatorKicksNameOnlyReachableTools(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile tool plans", err)
	for _, binding := range catalogfixture.LoadInformBindings(t).AllInform() {
		if binding == nil || !binding.IsInform() {
			continue
		}
		kickID := binding.Render
		if !strings.HasPrefix(kickID, "coordinator-") || slices.ContainsFunc(phaseKickPrefixes, func(p string) bool { return strings.HasPrefix(kickID, p) }) {
			continue
		}
		for id := range plans {
			t.Run(kickID+"/"+id, func(t *testing.T) {
				reader := coordinatorTestReader(t, id, 1)
				data := kickRenderFixture()
				delete(data, "tools")
				// The host digest quotes observations the reader already holds.
				delete(data, "evidence_digest")
				prompts.MergeCoordinatorKickVarsForTools(data, reader)
				corpus, err := engine.RenderKick(context.Background(), kickID, data)
				contractcheck.FailErr(t, "render kick", err)
				if names := unreachableNames(t, catalog, corpus, reader); len(names) > 0 {
					t.Errorf("names %s, which this surface cannot reach:\n%s", strings.Join(names, ", "), linesNaming(corpus, names))
				}
			})
		}
	}
}

type workflowPhaseBindings struct {
	Injects []struct {
		On       string `yaml:"on"`
		Render   string `yaml:"render"`
		Selector struct {
			Phase string `yaml:"phase"`
		} `yaml:"selector"`
	} `yaml:"injects"`
	Phases []struct {
		ID      string `yaml:"id"`
		Surface string `yaml:"coordinator_surface"`
	} `yaml:"phases"`
}

// Phase guidance opens on the surface its workflow declares for that phase.
func TestRenderedPhaseKicksNameOnlyTheirSurfacesTools(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile tool plans", err)
	root := contractcheck.RepoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "lycaon/config/packs/painted-wolf/*/workflows/*/workflow.yaml"))
	contractcheck.FailErr(t, "list workflows", err)
	if len(files) == 0 {
		t.Fatal("no stock workflows")
	}
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
			id := surfaces[inject.Selector.Phase]
			_, known := plans[id]
			if inject.On != "phase.entered" || !known {
				continue
			}
			t.Run(filepath.Base(filepath.Dir(file))+"/"+inject.Selector.Phase, func(t *testing.T) {
				reader := coordinatorTestReader(t, id, 1)
				data := kickRenderFixture()
				// The host digest quotes observations the reader already holds.
				delete(data, "evidence_digest")
				prompts.MergeCoordinatorKickVarsForTools(data, reader)
				corpus, err := engine.RenderKick(context.Background(), inject.Render, data)
				contractcheck.FailErr(t, "render kick", err)
				if names := unreachableNames(t, catalog, corpus, reader); len(names) > 0 {
					t.Errorf("%s on %s names %s:\n%s", inject.Render, id, strings.Join(names, ", "), linesNaming(corpus, names))
				}
			})
		}
	}
}

// A worker kick reaches every worker profile, so it names only what they hold.
func TestRenderedWorkerKicksNameOnlyWorkerTools(t *testing.T) {
	catalog, profiles := promptNameCatalog(t)
	postures, err := sessionprofiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "load postures", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, binding := range catalogfixture.LoadInformBindings(t).AllInform() {
		if binding == nil || !binding.IsInform() || !strings.HasPrefix(binding.Render, "worker-") {
			continue
		}
		for _, profile := range profiles {
			if !profile.Tools["complete_leg"] {
				continue
			}
			for _, posture := range postures.List() {
				t.Run(binding.Render+"/"+profile.ID+"/"+string(posture.ID), func(t *testing.T) {
					reader, err := prompts.NewReader(prompts.ReaderInput{
						ProfileID: profile.ID,
						Profiles:  postureProfiles(profiles, posture.CommandEffects),
						RootCount: 1,
						HasGit:    true,
						WebSearch: true,
					})
					contractcheck.FailErr(t, "build worker reader", err)
					data := kickRenderFixture()
					// The host digest quotes observations the reader already holds.
					delete(data, "evidence_digest")
					maps.Copy(data, reader.Facts())
					corpus, err := engine.RenderKick(context.Background(), binding.Render, data)
					contractcheck.FailErr(t, "render kick", err)
					if names := unreachableNames(t, catalog, corpus, reader); len(names) > 0 {
						t.Errorf("names %s:\n%s", strings.Join(names, ", "), linesNaming(corpus, names))
					}
				})
			}
		}
	}
}

func TestRenderedWorkerPersonasNameOnlyReachableTools(t *testing.T) {
	catalog, profiles := promptNameCatalog(t)
	postures, err := sessionprofiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "load postures", err)
	eff := contractcheck.StockCatalog(t)
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	contractcheck.FailErr(t, "load agents", err)
	engine := contractPersonaEngine(t)
	for _, agent := range agents {
		if agent.ID == "coordinator" {
			continue
		}
		for _, posture := range postures.List() {
			t.Run(agent.ID+"/"+string(posture.ID), func(t *testing.T) {
				idx := slices.IndexFunc(profiles, func(p sandbox.ToolProfile) bool { return p.ID == agent.ToolProfile })
				if idx < 0 {
					t.Skip("agent holds no tool profile")
				}
				reader, err := prompts.NewReader(prompts.ReaderInput{
					ProfileID: agent.ToolProfile,
					Profiles:  postureProfiles(profiles, posture.CommandEffects),
					RootCount: 1,
					HasGit:    true,
					WebSearch: true,
				})
				contractcheck.FailErr(t, "build worker reader", err)
				corpus, err := prompts.RenderPersona(context.Background(), engine, agent.ID, reader, nil)
				contractcheck.FailErr(t, "render persona", err)
				if names := unreachableNames(t, catalog, corpus, reader); len(names) > 0 {
					t.Errorf("names %s, which this profile does not hold:\n%s", strings.Join(names, ", "), linesNaming(corpus, names))
				}
			})
		}
	}
}
