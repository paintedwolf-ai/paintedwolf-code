package contract

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
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
	folderless, err := hoststate.FolderlessSurfaces()
	contractcheck.FailErr(t, "folderless surfaces", err)
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
			// The render attaches units for the widest turn, every loadable
			// tool loaded, so names are read against that turn's tools.
			held := reader.Held()
			for _, name := range plans[id].DeferredNames() {
				held[name] = true
			}
			check := func(t *testing.T, corpus string) {
				t.Helper()
				if names := toolvocab.UnreachableNames(catalog, corpus, held); len(names) > 0 {
					t.Errorf("names %s, which this surface cannot reach:\n%s", strings.Join(names, ", "), linesNaming(corpus, names))
				}
			}
			// With no folder attached, only the surfaces that reach no worktree open.
			if rootCount > 0 || slices.Contains(folderless, id) {
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

func TestRenderedAskModeCoordinatorPromptsMatchOfferedTools(t *testing.T) {
	catalog, profiles := promptNameCatalog(t)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	unitCatalog, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)

	for _, rootCount := range []int{0, 1, 2} {
		for _, workflowActive := range []bool{false, true} {
			name := fmt.Sprintf("%d-roots/workflow=%t", rootCount, workflowActive)
			t.Run(name, func(t *testing.T) {
				sess := &api.Session{Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild}
				var roots []projectroot.RootRef
				if rootCount >= 1 {
					sess.WorkspacePath = "/tmp/fixture-project"
					roots = append(roots, projectroot.RootRef{ID: "r1", Label: "main", Path: sess.WorkspacePath, IsPrimary: true})
				}
				if rootCount >= 2 {
					roots = append(roots, projectroot.RootRef{ID: "r2", Label: "den", Path: "/tmp/fixture-den", IsPrimary: false})
				}
				runCtx := api.CoordinatorRunContext{
					AllowedAgents: prompts.ReadOnlyAgents(spawn.AmbientAllowedAgents()),
				}
				if workflowActive {
					runCtx.RunID = "run-ask-1"
					runCtx.PhaseCoordinatorSurface = "observe_plan"
					runCtx.AdvanceWhenGateMet = "coordinator"
				}
				profile := surface.ResolveTurnProfile(runCtx, sess, nil)
				plan, err := surface.CompileToolPlan(profile, rootCount)
				contractcheck.FailErr(t, "compile Ask tool plan", err)
				if !workflowActive && plan.Addressable("ask_user") {
					t.Fatal("ambient Ask mode must not offer ask_user")
				}
				if workflowActive && !plan.Immediate("ask_user") {
					t.Fatal("active workflow Ask mode must offer ask_user")
				}
				reader, err := prompts.NewReader(prompts.ReaderInput{
					ProfileID:      prompts.CoordinatorProfileID,
					SurfaceID:      profile.SurfaceID,
					Mode:           surface.ExecutionModeFamily(profile.SurfaceID),
					SessionMode:    string(api.SessionModeAsk),
					Profiles:       profiles,
					Plan:           plan,
					RootCount:      rootCount,
					HasGit:         rootCount > 0,
					HasAnyGitRoot:  rootCount > 0,
					WebSearch:      true,
					SpawnAllowlist: runCtx.AllowedAgents,
				})
				contractcheck.FailErr(t, "new Ask reader", err)
				vars := mergeCoordinatorSurfaceVars(t, profile.SurfaceID)
				prompts.MergeWorkspaceRootsVars(vars, roots, "")
				transition := surface.ComputeModeTransition("", surface.ExecutionModeFamily(profile.SurfaceID), nil)
				contractcheck.FailErr(t, "merge coordinator prompt vars", prompts.MergeCoordinatorPromptVars(profile.SurfaceID, prompts.ExecutionModePromptTransition{
					ExecutionModePrevious: transition.ExecutionModePrevious,
					ExecutionModeEntered:  transition.ExecutionModeEntered,
					ExecutionModeLeft:     transition.ExecutionModeLeft,
				}, "", vars))
				mergeReachableSkillRoster(t, vars, reader)
				contractcheck.FailErr(t, "MergeForTurn", capability.MergeForTurn(vars, reader, runCtx.AllowedAgents))
				vars, err = prompts.Vars(reader, vars)
				contractcheck.FailErr(t, "prompts.Vars", err)
				blocks, err := prompts.RenderUnitSlots(t.Context(), prompts.UnitRendererFor(engine), unitCatalog, reader.Selection(), vars)
				contractcheck.FailErr(t, "RenderUnitSlots", err)
				prompts.MergeUnitVars(vars, blocks)

				var parts []string
				for _, ref := range append([]string{"agents/coordinator-core.md"}, modeTemplateRefs(profile)...) {
					chunk, renderErr := engine.Render(t.Context(), ref, vars)
					contractcheck.FailErr(t, "render "+ref, renderErr)
					if trimmed := strings.TrimSpace(chunk); trimmed != "" {
						parts = append(parts, trimmed)
					}
				}
				if profile.SurfaceTemplate != "" {
					chunk, renderErr := engine.Render(t.Context(), profile.SurfaceTemplate, vars)
					contractcheck.FailErr(t, "render "+profile.SurfaceTemplate, renderErr)
					if trimmed := strings.TrimSpace(chunk); trimmed != "" {
						parts = append(parts, trimmed)
					}
				}
				corpus := strings.Join(parts, "\n\n")
				held := reader.Held()
				if names := toolvocab.UnreachableNames(catalog, corpus, held); len(names) > 0 {
					t.Errorf("Ask mode prompt names unreachable tools %v:\n%s", names, linesNaming(corpus, names))
				}
				for _, cmdUtility := range []string{"`cat -n", "`grep -n", "`rg -n", "`sed -n"} {
					if strings.Contains(corpus, cmdUtility) {
						t.Errorf("Ask mode prompt teaches command utility %s when command is not offered:\n%s", cmdUtility, corpus)
					}
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
