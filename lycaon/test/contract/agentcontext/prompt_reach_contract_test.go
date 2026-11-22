package contract

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
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
	catalog, _ := promptNameCatalog(t)
	prunable, err := toolvocabtest.StockPrunableSchemaIndex()
	contractcheck.FailErr(t, "stock prunable index", err)
	vocab, err := toolvocabtest.StockBacktickVocab()
	contractcheck.FailErr(t, "stock backtick vocab", err)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	unitCatalog, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)

	for _, rootCount := range []int{0, 1, 2} {
		for _, workflowActive := range []bool{false, true} {
			name := fmt.Sprintf("%d-roots/workflow=%t", rootCount, workflowActive)
			t.Run(name, func(t *testing.T) {
				sess := &api.Session{Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild}
				var roots []projectroot.RootRef
				var rootSpecs []hoststate.RootSpec
				if rootCount >= 1 {
					sess.WorkspacePath = "/tmp/fixture-project"
					r1 := projectroot.RootRef{ID: "r1", Label: "main", Path: sess.WorkspacePath, IsPrimary: true}
					roots = append(roots, r1)
					rootSpecs = append(rootSpecs, hoststate.RootSpec{Ref: r1, HasGit: true})
				}
				if rootCount >= 2 {
					r2 := projectroot.RootRef{ID: "r2", Label: "den", Path: "/tmp/fixture-den", IsPrimary: false}
					roots = append(roots, r2)
					rootSpecs = append(rootSpecs, hoststate.RootSpec{Ref: r2, HasGit: true})
				}
				runCtx := api.CoordinatorRunContext{
					AllowedAgents: prompts.ReadOnlyAgents(spawn.AmbientAllowedAgents()),
				}
				if workflowActive {
					runCtx.RunID = "run-ask-1"
					runCtx.WorkflowID = "fixture-workflow"
					runCtx.PhaseCoordinatorSurface = "observe_plan"
					runCtx.AdvanceWhenGateMet = "coordinator"
				}
				profile := surface.ResolveTurnProfile(runCtx, sess, nil)
				auth, err := hoststate.FreezeTurnAuthority(t.Context(), hoststate.CoordinatorState{
					SessionMode:    api.SessionModeAsk,
					Posture:        api.SessionPostureBuild,
					WebSearch:      true,
					Roots:          rootSpecs,
					RootCount:      rootCount,
					HasGit:         rootCount > 0,
					HasAnyGitRoot:  rootCount > 0,
					SpawnAllowlist: runCtx.AllowedAgents,
					RunContext:     runCtx,
					TurnProfile:    profile,
				})
				contractcheck.FailErr(t, "freeze Ask turn authority", err)
				if !workflowActive && auth.Plan.Addressable("ask_user") {
					t.Fatal("ambient Ask mode must not offer ask_user")
				}
				if workflowActive && !auth.Plan.Immediate("ask_user") {
					t.Fatal("active workflow Ask mode must offer ask_user")
				}
				reader := auth.Reader
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
				violations := toolvocabtest.EvaluateClaimBlock(toolvocabtest.ClaimEvaluationContext{
					Catalog:     catalog,
					Prunable:    prunable,
					Vocab:       vocab,
					Self:        toolvocabtest.AuthorityFromReaderAndMetas(auth.Reader, auth.ProgramReach, auth.Metas),
					SessionMode: api.SessionModeAsk,
					Posture:     api.SessionPostureBuild,
				}, toolvocabtest.ClaimBlock{
					Label:   "ask/" + name,
					Subject: toolvocabtest.SubjectSelfNow(),
					Text:    corpus,
				})
				for _, v := range violations {
					t.Errorf("Ask mode prompt violation (%s at line %d): %s — %s", v.Claim, v.Line, v.Reason, linesNaming(corpus, []string{v.Claim}))
				}
			})
		}
	}
}

func TestAskModeCommandUtilityClaimsFollowResolvedAuthorityAcrossRevisions(t *testing.T) {
	eff := contractcheck.StockCatalog(t)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	contractcheck.FailErr(t, "load profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	contractcheck.FailErr(t, "load schemas", err)
	catalog, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	contractcheck.FailErr(t, "catalog", err)
	prunable, err := toolvocabtest.StockPrunableSchemaIndex()
	contractcheck.FailErr(t, "prunable", err)
	vocab, err := toolvocabtest.StockBacktickVocab()
	contractcheck.FailErr(t, "vocab", err)

	roEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	contractcheck.FailErr(t, "ro effects", err)
	narrowedProfiles := slices.Clone(profiles)
	for i := range narrowedProfiles {
		narrowedProfiles[i].CommandEffects &= roEffects
	}

	cmdMeta, ok := schemas.ToolMeta("command")
	if !ok {
		t.Fatal("missing command schema")
	}
	askMeta, ok := schemas.ToolMeta("ask_user")
	if !ok {
		t.Fatal("missing ask_user schema")
	}
	roCommandSchema := tools.PruneCommandReadOnlySchema(cmdMeta.ArgsSchema)
	askMetas := promptloop.ProjectAskToolMetas([]tools.ToolMeta{
		{Name: "ask_user", ArgsSchema: askMeta.ArgsSchema},
		{Name: "command", ArgsSchema: roCommandSchema},
	})

	t.Run("v1-baseline Ask withholds command and rejects command utilities", func(t *testing.T) {
		v1Plan := toolsurface.Compile([]string{"recall", "source_history", "ask_user"}, nil)
		v1Reader, rErr := prompts.NewReader(prompts.ReaderInput{
			ProfileID:   prompts.CoordinatorProfileID,
			SurfaceID:   "observe_investigate",
			SessionMode: string(api.SessionModeAsk),
			Profiles:    narrowedProfiles,
			Plan:        v1Plan,
			Visible:     []string{"recall", "source_history", "ask_user"},
			Schemas:     schemas,
			RootCount:   1,
			HasGit:      true,
		})
		contractcheck.FailErr(t, "v1 reader", rErr)
		evalCtx := toolvocabtest.ClaimEvaluationContext{
			Catalog:     catalog,
			Prunable:    prunable,
			Vocab:       vocab,
			Self:        toolvocabtest.AuthorityFromReaderAndMetas(v1Reader, nil, askMetas[:1]),
			SessionMode: api.SessionModeAsk,
			Posture:     api.SessionPostureBuild,
		}
		if got := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
			Label: "v1-valid", Subject: toolvocabtest.SubjectSelfNow(),
			Text: "Use `source_history` to inspect recorded changes and `ask_user(prompt=\"Clarify?\")` when needed.",
		}); len(got) > 0 {
			t.Fatalf("unexpected v1-baseline violations: %+v", got)
		}
		for _, badUtility := range []string{"`cat -n FILE`", "`grep -n PAT FILE`", "`rg -n PAT`", "`sed -n '1,20p' FILE`"} {
			if got := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
				Label: "v1-cmd", Subject: toolvocabtest.SubjectSelfNow(),
				Text: "Inspect lines with " + badUtility + ".",
			}); len(got) == 0 {
				t.Fatalf("expected v1-baseline Ask authority without command to reject %s", badUtility)
			}
		}
	})

	t.Run("v2-ro02-option1 Ask offers read-only command and rejects mutating or pruned forms", func(t *testing.T) {
		v2Plan := toolsurface.Compile([]string{"recall", "command", "source_history", "ask_user"}, nil)
		v2Reader, rErr := prompts.NewReader(prompts.ReaderInput{
			ProfileID:   prompts.CoordinatorProfileID,
			SurfaceID:   "observe_investigate",
			SessionMode: string(api.SessionModeAsk),
			Profiles:    narrowedProfiles,
			Plan:        v2Plan,
			Visible:     []string{"recall", "command", "source_history", "ask_user"},
			Schemas:     schemas,
			RootCount:   1,
			HasGit:      true,
		})
		contractcheck.FailErr(t, "v2 reader", rErr)
		evalCtx := toolvocabtest.ClaimEvaluationContext{
			Catalog:     catalog,
			Prunable:    prunable,
			Vocab:       vocab,
			Self:        toolvocabtest.AuthorityFromReaderAndMetas(v2Reader, nil, askMetas),
			SessionMode: api.SessionModeAsk,
			Posture:     api.SessionPostureBuild,
		}
		if got := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
			Label: "v2-readonly-utilities", Subject: toolvocabtest.SubjectSelfNow(),
			Text: "Inspect files with `cat -n FILE`, `grep -n PAT FILE`, `rg -n PAT`, `sed -n '1,20p' FILE`, and `git status` in `command`.",
		}); len(got) > 0 {
			t.Fatalf("expected v2-ro02-option1 Ask authority to admit read-only command utilities, got %+v", got)
		}
		for _, forbidden := range []string{
			"Apply changes with `edit --path a.go --old-string x --new-string y`.",
			"Commit via `git commit -m msg -- a.go`.",
			"Fetch with `curl -s https://example.com`.",
			"Run `external -- ls`.",
			"Run `command(command=\"./task test:short\", verification=true)`.",
			"Declare `capability_request.local_listen` on `command`.",
			"Ask secret via `ask_user(response_type=\"secret\", …)`.",
		} {
			if got := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
				Label: "v2-forbidden", Subject: toolvocabtest.SubjectSelfNow(),
				Text: forbidden,
			}); len(got) == 0 {
				t.Fatalf("expected v2-ro02-option1 Ask authority to reject %q", forbidden)
			}
		}
	})
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
