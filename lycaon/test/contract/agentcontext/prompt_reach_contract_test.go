package contract

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
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
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
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

// Coordinator standing prompts, attached units and tool procedures name only
// what each coordinator host state can reach.
func TestRenderedCoordinatorPromptsNameOnlyReachableTools(t *testing.T) {
	ctx := t.Context()
	catalog, _ := promptNameCatalog(t)
	prunable, err := toolvocabtest.StockPrunableSchemaIndex()
	contractcheck.FailErr(t, "stock prunable schema index", err)
	vocab, err := toolvocabtest.StockBacktickVocab()
	contractcheck.FailErr(t, "stock backtick vocab", err)
	folderless, err := hoststate.FolderlessSurfaces()
	contractcheck.FailErr(t, "folderless surfaces", err)

	engine := contractcheck.BundledPromptEngineForRoot(t)
	injRenderer := prompts.NewInjectRenderer(engine)
	unitCatalog, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)
	baseSpawnVars := spawn.PolicyTemplateVars()

	agentDefs, err := agentdef.LoadEffectiveWithCatalog(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "load agent defs", err)
	agentProfiles := make(map[string]string, len(agentDefs))
	for _, d := range agentDefs {
		agentProfiles[d.ID] = d.ToolProfile
	}
	workerMemo := map[string]toolvocabtest.ActorAuthority{}

	coordStates, err := hoststate.CoordinatorStates(ctx)
	contractcheck.FailErr(t, "CoordinatorStates", err)

	cache := hoststate.NewRenderCache()
	seenPostures := map[api.SessionPosture]int{}
	seenModes := map[api.SessionMode]int{}
	seenLoadings := map[hoststate.LoadingMode]int{}
	seenSurfaces := map[string]int{}
	seenTopologies := map[hoststate.RootTopology]int{}
	verifiedFolderlessPark := false

	for _, st := range coordStates {
		if st.Excluded || st.PackOverlayID != "stock" {
			continue
		}
		if st.RootCount == 0 && st.TurnProfile.SurfaceID != "implement_park" && !slices.Contains(folderless, st.TurnProfile.SurfaceID) {
			t.Fatalf("0-root state %s resolved to non-folderless surface %q", st.ID, st.TurnProfile.SurfaceID)
		}
		auth, fErr := hoststate.FreezeTurnAuthority(ctx, st)
		contractcheck.FailErr(t, "FreezeTurnAuthority "+st.ID, fErr)

		// Deferred tools that are not loaded are never held.
		if st.Loading == hoststate.LoadingFloorOnly {
			for _, defName := range auth.Plan.DeferredNames() {
				if auth.Reader.Held()[defName] {
					t.Fatalf("floor_only state %s marked deferred tool %q as held in Reader", st.ID, defName)
				}
			}
		}
		if st.Loading == hoststate.LoadingPartlyLoaded && len(auth.Plan.DeferredNames()) > 1 {
			unloaded := 0
			for _, defName := range auth.Plan.DeferredNames() {
				if !st.Loaded[defName] {
					unloaded++
					if auth.Reader.Held()[defName] {
						t.Fatalf("partly_loaded state %s marked unloaded deferred tool %q as held", st.ID, defName)
					}
				}
			}
			if unloaded == 0 {
				t.Fatalf("partly_loaded state %s loaded all deferred tools (%v)", st.ID, auth.Plan.DeferredNames())
			}
		}
		if st.SessionMode == api.SessionModeAgent && st.RootCount == 0 && st.WorkflowState == hoststate.WorkflowWorkersInFlight {
			if st.TurnProfile.SurfaceID != "implement_park" || auth.Reader.Held()["pack_board"] {
				t.Fatalf("0-root WorkflowWorkersInFlight resolved to surface=%q pack_board=%t", st.TurnProfile.SurfaceID, auth.Reader.Held()["pack_board"])
			}
			verifiedFolderlessPark = true
		}

		seenPostures[st.Posture]++
		seenModes[st.SessionMode]++
		seenLoadings[st.Loading]++
		seenSurfaces[st.TurnProfile.SurfaceID]++
		seenTopologies[st.RootTopology]++

		resolveNamed := func(agentID string) (toolvocabtest.ActorAuthority, error) {
			profID, ok := agentProfiles[agentID]
			if !ok {
				return toolvocabtest.ActorAuthority{}, fmt.Errorf("unknown agent %q", agentID)
			}
			mKey := fmt.Sprintf("%s|%s|%d|%t|%t|%t|%t|%s", st.SessionMode, st.Posture, st.RootCount, st.HasGit, st.HasAnyGitRoot, st.WebSearch, st.CommandExternalHidden, agentID)
			if cached, found := workerMemo[mKey]; found {
				return cached, nil
			}
			wAuth, wErr := hoststate.FreezeWorkerAuthority(ctx, hoststate.WorkerState{
				AgentID: agentID, ToolProfile: profID, ParentSessionMode: st.SessionMode,
				Posture: st.Posture, RootCount: st.RootCount, HasGit: st.HasGit,
				HasAnyGitRoot: st.HasAnyGitRoot, WebSearch: st.WebSearch,
				CommandExternalHidden: st.CommandExternalHidden, Overlay: st.Overlay,
			})
			if wErr != nil {
				return toolvocabtest.ActorAuthority{}, wErr
			}
			actor := wAuth.ActorAuthority()
			workerMemo[mKey] = actor
			return actor, nil
		}

		vars := buildStandingVarsForState(t, baseSpawnVars, st, auth.Reader)
		unitIDs, unitRefs := unitIDsAndRefsForSelection(unitCatalog, auth.Reader.Selection())
		standingRefs := append([]string{"agents/coordinator-core.md", st.TurnProfile.SurfaceTemplate}, modeTemplateRefs(st.TurnProfile)...)
		standingRefs = append(standingRefs, unitRefs...)
		keyVars := promptrender.ProjectRenderVars(st.Overlay.Catalog, vars, standingRefs...)
		rKey := hoststate.RenderCacheKey("ac-standing:"+st.TurnProfile.SurfaceID, st.Overlay.Catalog.Revision, unitIDs, keyVars)
		standingCorpus, _, rErr := cache.RenderOrReuse(rKey, func() (string, error) {
			return renderStandingWithUnits(ctx, engine, unitCatalog, st.TurnProfile, auth.Reader.Selection(), vars)
		})
		contractcheck.FailErr(t, "render standing "+st.ID, rErr)

		_ = cache.CheckAuthority(rKey, auth.Digest, func() error {
			return checkCorpusAgainstAuthority(t, catalog, prunable, vocab, "standing/"+st.ID, standingCorpus, auth, st.SessionMode, st.Posture, resolveNamed)
		})

		procUnits, pErr := inject.ToolProcedureUnits(injRenderer, auth.Reader)
		contractcheck.FailErr(t, "ToolProcedureUnits "+st.ID, pErr)
		procIDs := make([]string, len(procUnits))
		procRefs := make([]string, len(procUnits))
		for i, u := range procUnits {
			procIDs[i] = u.ID
			procRefs[i] = u.Ref
		}
		procVars, pvErr := prompts.Vars(auth.Reader, map[string]any{"offered_tools": auth.Reader.Offered()})
		contractcheck.FailErr(t, "proc Vars "+st.ID, pvErr)
		procKeyVars := promptrender.ProjectRenderVars(st.Overlay.Catalog, procVars, procRefs...)
		pKey := hoststate.RenderCacheKey("ac-proc", st.Overlay.Catalog.Revision, procIDs, procKeyVars)
		procBlock, _, pbErr := cache.RenderOrReuse(pKey, func() (string, error) {
			return inject.RenderToolProceduresBlock(ctx, injRenderer, "sess-ac", auth.Reader)
		})
		contractcheck.FailErr(t, "RenderToolProceduresBlock "+st.ID, pbErr)
		if strings.TrimSpace(procBlock) != "" {
			_ = cache.CheckAuthority(pKey, auth.Digest, func() error {
				return checkCorpusAgainstAuthority(t, catalog, prunable, vocab, "procedures/"+st.ID, procBlock, auth, st.SessionMode, st.Posture, resolveNamed)
			})
		}
	}

	for _, wantPosture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureBuild, api.SessionPostureOrchestrate, api.SessionPostureVet} {
		if seenPostures[wantPosture] == 0 {
			t.Errorf("missing posture %q in coordinator reach sweep", wantPosture)
		}
	}
	for _, wantMode := range []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk} {
		if seenModes[wantMode] == 0 {
			t.Errorf("missing session mode %q in coordinator reach sweep", wantMode)
		}
	}
	for _, wantLoad := range []hoststate.LoadingMode{hoststate.LoadingFloorOnly, hoststate.LoadingPartlyLoaded, hoststate.LoadingAllLoaded} {
		if seenLoadings[wantLoad] == 0 {
			t.Errorf("missing loading state %q in coordinator reach sweep", wantLoad)
		}
	}
	for _, wantTopo := range []hoststate.RootTopology{hoststate.RootsNone, hoststate.RootsSingleGit, hoststate.RootsDualGitFirst, hoststate.RootsDualNonGitFirst} {
		if seenTopologies[wantTopo] == 0 {
			t.Errorf("missing root topology %q in coordinator reach sweep", wantTopo)
		}
	}
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "CompileToolPlans", err)
	for id := range plans {
		if seenSurfaces[id] == 0 {
			t.Errorf("coordinator surface %s not reached by the sweep", id)
		}
	}
	if !verifiedFolderlessPark {
		t.Error("expected 0-root WorkflowWorkersInFlight -> implement_park cell to be verified")
	}
}

func buildStandingVarsForState(t *testing.T, baseSpawnVars map[string]any, st hoststate.CoordinatorState, reader prompts.Reader) map[string]any {
	t.Helper()
	occ := maps.Clone(baseSpawnVars)
	if len(st.Roots) > 0 {
		refs := make([]projectroot.RootRef, len(st.Roots))
		activeID := ""
		for i, r := range st.Roots {
			refs[i] = r.Ref
			if r.Ref.IsPrimary {
				activeID = r.Ref.ID
			}
		}
		prompts.MergeWorkspaceRootsVars(occ, refs, activeID)
	} else {
		prompts.MergeWorkspaceRootsVars(occ, nil, "")
	}
	transition := surface.ComputeModeTransition("", surface.ExecutionModeFamily(st.TurnProfile.SurfaceID), nil)
	contractcheck.FailErr(t, "MergeCoordinatorPromptVars", prompts.MergeCoordinatorPromptVars(st.TurnProfile.SurfaceID, prompts.ExecutionModePromptTransition{
		ExecutionModePrevious: transition.ExecutionModePrevious,
		ExecutionModeEntered:  transition.ExecutionModeEntered,
		ExecutionModeLeft:     transition.ExecutionModeLeft,
	}, "", occ))
	mergeReachableSkillRoster(t, occ, reader)
	contractcheck.FailErr(t, "MergeForTurn", capability.MergeForTurn(occ, reader, st.SpawnAllowlist))
	cleanOcc := make(map[string]any, len(occ))
	for k, v := range occ {
		if !prompts.IsDeclaredOrReservedReaderFact(k) {
			cleanOcc[k] = v
		}
	}
	vars, err := prompts.Vars(reader, cleanOcc)
	contractcheck.FailErr(t, "prompts.Vars", err)
	return vars
}

func unitIDsAndRefsForSelection(cat promptunit.Catalog, sel promptunit.Selection) ([]string, []string) {
	rendered := cat.Rendered(sel)
	var ids, refs []string
	for _, slot := range promptunit.Slots() {
		for _, u := range rendered[slot] {
			ids = append(ids, u.ID)
			refs = append(refs, u.Ref)
		}
	}
	return ids, refs
}

func renderStandingWithUnits(
	ctx context.Context,
	engine prompts.PromptTemplateEngine,
	unitCatalog promptunit.Catalog,
	profile surface.TurnProfile,
	sel promptunit.Selection,
	baseVars map[string]any,
) (string, error) {
	vars := maps.Clone(baseVars)
	blocks, err := prompts.RenderUnitSlots(ctx, prompts.UnitRendererFor(engine), unitCatalog, sel, vars)
	if err != nil {
		return "", err
	}
	prompts.MergeUnitVars(vars, blocks)
	var parts []string
	for _, ref := range append([]string{"agents/coordinator-core.md"}, modeTemplateRefs(profile)...) {
		if ref == "" {
			continue
		}
		chunk, rErr := engine.Render(ctx, ref, vars)
		if rErr != nil {
			return "", rErr
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if profile.SurfaceTemplate != "" {
		chunk, rErr := engine.Render(ctx, profile.SurfaceTemplate, vars)
		if rErr != nil {
			return "", rErr
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func checkCorpusAgainstAuthority(
	t *testing.T,
	catalog *toolvocab.Catalog,
	prunable *toolvocabtest.PrunableSchemaIndex,
	vocab *toolvocabtest.BacktickVocab,
	label, corpus string,
	auth hoststate.TurnAuthority,
	mode api.SessionMode,
	posture api.SessionPosture,
	resolveNamed func(string) (toolvocabtest.ActorAuthority, error),
) error {
	t.Helper()
	violations := toolvocabtest.EvaluateClaimBlock(toolvocabtest.ClaimEvaluationContext{
		Catalog:           catalog,
		Prunable:          prunable,
		Vocab:             vocab,
		Self:              auth.ActorAuthority(),
		SessionMode:       mode,
		Posture:           posture,
		ResolveNamedAgent: resolveNamed,
	}, toolvocabtest.ClaimBlock{
		Label:   label,
		Subject: toolvocabtest.SubjectSelfNow(),
		Text:    corpus,
	})
	for _, v := range violations {
		t.Errorf("%s violation (`%s` at line %d): %s — %s", label, v.Claim, v.Line, v.Reason, linesNaming(corpus, []string{v.Claim}))
	}
	if len(violations) > 0 {
		return fmt.Errorf("%d violations on %s", len(violations), label)
	}
	return nil
}

func TestRenderedAskModeCoordinatorPromptsMatchOfferedTools(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	prunable, err := toolvocabtest.StockPrunableSchemaIndex()
	contractcheck.FailErr(t, "stock prunable index", err)
	vocab, err := toolvocabtest.StockBacktickVocab()
	contractcheck.FailErr(t, "stock backtick vocab", err)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	injRenderer := prompts.NewInjectRenderer(engine)
	unitCatalog, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)

	for _, posture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureBuild, api.SessionPostureOrchestrate, api.SessionPostureVet} {
		for _, rootCount := range []int{0, 1, 2} {
			for _, workflowActive := range []bool{false, true} {
				if rootCount == 0 && workflowActive {
					continue
				}
				name := fmt.Sprintf("%s/%d-roots/workflow=%t", posture, rootCount, workflowActive)
				t.Run(name, func(t *testing.T) {
					sess := &api.Session{Mode: api.SessionModeAsk, Posture: posture}
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
						rootSpecs = append(rootSpecs, hoststate.RootSpec{Ref: r2, HasGit: false})
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
					auth, fErr := hoststate.FreezeTurnAuthority(t.Context(), hoststate.CoordinatorState{
						SessionMode:    api.SessionModeAsk,
						Posture:        posture,
						WebSearch:      true,
						Roots:          rootSpecs,
						RootCount:      rootCount,
						HasGit:         rootCount > 0,
						HasAnyGitRoot:  rootCount > 0,
						SpawnAllowlist: runCtx.AllowedAgents,
						RunContext:     runCtx,
						TurnProfile:    profile,
					})
					contractcheck.FailErr(t, "freeze Ask turn authority", fErr)
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
					vars, fErr = prompts.Vars(reader, vars)
					contractcheck.FailErr(t, "prompts.Vars", fErr)
					corpus, rErr := renderStandingWithUnits(t.Context(), engine, unitCatalog, profile, reader.Selection(), vars)
					contractcheck.FailErr(t, "render Ask standing", rErr)
					_ = checkCorpusAgainstAuthority(t, catalog, prunable, vocab, "ask/"+name, corpus, auth, api.SessionModeAsk, posture, nil)

					procBlock, pErr := inject.RenderToolProceduresBlock(t.Context(), injRenderer, "sess-ask", reader)
					contractcheck.FailErr(t, "RenderToolProceduresBlock Ask", pErr)
					if strings.TrimSpace(procBlock) != "" {
						_ = checkCorpusAgainstAuthority(t, catalog, prunable, vocab, "ask-proc/"+name, procBlock, auth, api.SessionModeAsk, posture, nil)
					}
				})
			}
		}
	}
}

// An Ask coordinator, which is not offered command, is told no command
// utilities.
func TestAskModeWithholdsCommandUtilities(t *testing.T) {
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

	askMeta, ok := schemas.ToolMeta("ask_user")
	if !ok {
		t.Fatal("missing ask_user schema")
	}
	askMetas := promptloop.ProjectAskToolMetas([]tools.ToolMeta{{Name: "ask_user", ArgsSchema: askMeta.ArgsSchema}})

	plan := toolsurface.Compile([]string{"recall", "source_history", "ask_user"}, nil)
	reader, rErr := prompts.NewReader(prompts.HostFacts{
		SessionMode: string(api.SessionModeAsk),
		Profiles:    narrowedProfiles,
		Schemas:     schemas,
		RootCount:   1,
		HasGit:      true,
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "observe_investigate",
		Plan:      plan,
		Offered:   []string{"recall", "source_history", "ask_user"},
	})
	contractcheck.FailErr(t, "ask reader", rErr)
	evalCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog:     catalog,
		Prunable:    prunable,
		Vocab:       vocab,
		Self:        toolvocabtest.AuthorityFromReaderAndMetas(reader, nil, askMetas),
		SessionMode: api.SessionModeAsk,
		Posture:     api.SessionPostureBuild,
	}
	if got := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
		Label: "ask-valid", Subject: toolvocabtest.SubjectSelfNow(),
		Text: "Use `source_history` to inspect recorded changes and `ask_user(prompt=\"Clarify?\")` when needed.",
	}); len(got) > 0 {
		t.Fatalf("unexpected Ask violations: %+v", got)
	}
	for _, badUtility := range []string{"`cat -n FILE`", "`grep -n PAT FILE`", "`rg -n PAT`", "`sed -n '1,20p' FILE`"} {
		if got := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
			Label: "ask-command-utility", Subject: toolvocabtest.SubjectSelfNow(),
			Text: "Inspect lines with " + badUtility + ".",
		}); len(got) == 0 {
			t.Fatalf("Ask authority without command admitted %s", badUtility)
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
