package hoststate_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func TestReaderHeldMatchesMetadataProgramReachAcrossHostStates(t *testing.T) {
	overlays, err := hoststate.DiscoverPackOverlays(t.Context())
	contractcheck.FailErr(t, "DiscoverPackOverlays", err)
	stock := overlays[0]

	postures, err := hoststate.Postures(stock.Catalog)
	contractcheck.FailErr(t, "Postures", err)
	surfaces, err := hoststate.CoordinatorSurfaceIDs()
	contractcheck.FailErr(t, "CoordinatorSurfaceIDs", err)
	lang, err := toolcontract.ReadOnlyLanguageV1()
	contractcheck.FailErr(t, "ReadOnlyLanguageV1", err)
	if len(lang.Commands) != 176 {
		t.Fatalf("len(ReadOnlyLanguageV1().Commands) = %d, want 176", len(lang.Commands))
	}

	checkedTuples := 0
	for _, posture := range postures {
		for _, externalHidden := range []bool{false, true} {
			exec, narrowed, execErr := hoststate.ExecutorForOverlay(stock, posture.ID, false, true, externalHidden, hoststate.MCPNone)
			contractcheck.FailErr(t, "ExecutorForOverlay", execErr)

			for _, profile := range narrowed {
				surfaceList := []string{""}
				if profile.ID == prompts.CoordinatorProfileID {
					surfaceList = surfaces
				}
				for _, surfaceID := range surfaceList {
					checkedTuples++
					reader := buildWidestTupleReader(t, narrowed, stock, profile, surfaceID, externalHidden)
					held := reader.Held()

					progReach, reachErr := exec.Metadata.ProgramReach(t.Context(), platform.ToolFilter{
						ProfileID: profile.ID,
						SurfaceID: surfaceID,
					})
					contractcheck.FailErr(t, "Metadata.ProgramReach", reachErr)

					for _, cmd := range lang.Commands {
						got := toolcontract.CommandLanguageReachable(cmd.Name, held)
						gotReader := reader.CanReachProgram(cmd.Name)
						want := held["command"] && progReach(cmd.Name)
						if got != want || gotReader != want {
							t.Fatalf(
								"posture=%s profile=%s surface=%q externalHidden=%t command=%q: CommandLanguageReachable=%t, Reader.CanReachProgram=%t, Metadata.ProgramReach=%t",
								posture.ID, profile.ID, surfaceID, externalHidden, cmd.Name, got, gotReader, want,
							)
						}
					}
				}
			}
		}
	}
	if checkedTuples == 0 {
		t.Fatal("checked 0 (posture, profile, surface) tuples")
	}

	verifyAuthorityProjections(t, stock)
}

func buildWidestTupleReader(
	t *testing.T,
	profiles []sandbox.ToolProfile,
	stock hoststate.PackOverlay,
	profile sandbox.ToolProfile,
	surfaceID string,
	externalHidden bool,
) prompts.Reader {
	t.Helper()
	loaded := map[string]bool{}
	var planInput prompts.ReaderInput
	if surfaceID != "" {
		plan, err := prompts.CompileSurfacePlan(surfaceID, 1)
		contractcheck.FailErr(t, "CompileSurfacePlan "+surfaceID, err)
		for _, name := range plan.DeferredNames() {
			loaded[name] = true
		}
		planInput = prompts.ReaderInput{
			ProfileID:             profile.ID,
			SurfaceID:             surfaceID,
			Mode:                  surface.ExecutionModeFamily(surfaceID),
			Profiles:              profiles,
			Plan:                  plan,
			Loaded:                loaded,
			Schemas:               stock.Schemas,
			RootCount:             1,
			HasGit:                true,
			HasAnyGitRoot:         true,
			WebSearch:             true,
			SpawnAllowlist:        stock.SpawnAllowlist,
			CommandExternalHidden: externalHidden,
		}
	} else {
		for name := range profile.DeferredTools {
			if !strings.HasSuffix(name, "*") {
				loaded[name] = true
			}
		}
		planInput = prompts.ReaderInput{
			ProfileID:             profile.ID,
			Profiles:              profiles,
			Loaded:                loaded,
			Schemas:               stock.Schemas,
			RootCount:             1,
			HasGit:                true,
			HasAnyGitRoot:         true,
			WebSearch:             true,
			CommandExternalHidden: externalHidden,
		}
	}
	reader, err := prompts.NewReader(planInput)
	contractcheck.FailErr(t, "prompts.NewReader", err)
	return reader
}

func TestCommandExternalSelectorVisibilityWitness(t *testing.T) {
	overlays, err := hoststate.DiscoverPackOverlays(t.Context())
	contractcheck.FailErr(t, "DiscoverPackOverlays", err)
	stock := overlays[0]

	invProfile := surface.ResolveTurnProfileForSurface("implement_investigate", api.CoordinatorRunContext{}, &api.Session{
		Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
	})
	invPlan, err := surface.CompileToolPlan(invProfile, 1)
	contractcheck.FailErr(t, "CompileToolPlan implement_investigate", err)

	for _, externalHidden := range []bool{false, true} {
		wantExternal := !externalHidden
		coordAuth, err := hoststate.FreezeTurnAuthority(t.Context(), hoststate.CoordinatorState{
			Overlay:               stock,
			SessionMode:           api.SessionModeAgent,
			Posture:               api.SessionPostureBuild,
			RootCount:             1,
			HasGit:                true,
			HasAnyGitRoot:         true,
			WebSearch:             true,
			CommandExternalHidden: externalHidden,
			TurnProfile:           invProfile,
			Plan:                  invPlan,
			SpawnAllowlist:        stock.SpawnAllowlist,
		})
		contractcheck.FailErr(t, "FreezeTurnAuthority", err)
		if got := coordAuth.Reader.CanReachProgram("external"); got != wantExternal || coordAuth.ProgramReach("external") != wantExternal {
			t.Fatalf("coordinator externalHidden=%t: Reader.CanReachProgram(external)=%t ProgramReach(external)=%t, want %t",
				externalHidden, got, coordAuth.ProgramReach("external"), wantExternal)
		}
		if !coordAuth.Reader.CanReachProgram("private-service") || !coordAuth.ProgramReach("private-service") {
			t.Fatalf("coordinator externalHidden=%t: private-service became unreachable (Reader=%t ProgramReach=%t)",
				externalHidden, coordAuth.Reader.CanReachProgram("private-service"), coordAuth.ProgramReach("private-service"))
		}

		workerAuth, err := hoststate.FreezeWorkerAuthority(t.Context(), hoststate.WorkerState{
			Overlay:               stock,
			AgentID:               "implementer",
			ToolProfile:           "implement",
			Posture:               api.SessionPostureBuild,
			ParentSessionMode:     api.SessionModeAgent,
			RootCount:             1,
			HasGit:                true,
			HasAnyGitRoot:         true,
			OnWorkerBranch:        true,
			WebSearch:             true,
			CommandExternalHidden: externalHidden,
		})
		contractcheck.FailErr(t, "FreezeWorkerAuthority", err)
		if got := workerAuth.Reader.CanReachProgram("external"); got != wantExternal || workerAuth.ProgramReach("external") != wantExternal {
			t.Fatalf("worker externalHidden=%t: Reader.CanReachProgram(external)=%t ProgramReach(external)=%t, want %t",
				externalHidden, got, workerAuth.ProgramReach("external"), wantExternal)
		}
		if !workerAuth.Reader.CanReachProgram("private-service") || !workerAuth.ProgramReach("private-service") {
			t.Fatalf("worker externalHidden=%t: private-service became unreachable (Reader=%t ProgramReach=%t)",
				externalHidden, workerAuth.Reader.CanReachProgram("private-service"), workerAuth.ProgramReach("private-service"))
		}
	}
}

func verifyAuthorityProjections(t *testing.T, stock hoststate.PackOverlay) {
	t.Helper()
	askRunCtx := api.CoordinatorRunContext{RunID: "run-ask", PhaseCoordinatorSurface: "observe_plan"}
	askProfile := surface.ResolveTurnProfile(
		askRunCtx,
		&api.Session{Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild},
		nil,
	)
	askPlan, err := surface.CompileToolPlan(askProfile, 1)
	contractcheck.FailErr(t, "CompileToolPlan Ask", err)
	askState := hoststate.CoordinatorState{
		Overlay:        stock,
		SessionMode:    api.SessionModeAsk,
		Posture:        api.SessionPostureBuild,
		RootCount:      1,
		HasGit:         true,
		WebSearch:      true,
		RunContext:     askRunCtx,
		TurnProfile:    askProfile,
		Plan:           askPlan,
		SpawnAllowlist: prompts.ReadOnlyAgents(stock.SpawnAllowlist),
	}
	askAuth, err := hoststate.FreezeTurnAuthority(t.Context(), askState)
	contractcheck.FailErr(t, "FreezeTurnAuthority Ask", err)
	askSchema, ok := askAuth.Schemas["ask_user"]
	if !ok {
		t.Fatal("expected ask_user offered in active workflow Ask state")
	}
	props, _ := askSchema["properties"].(map[string]any)
	if _, hasSecret := props["secret"]; hasSecret {
		t.Fatal("ProjectAskToolMetas did not strip secret property from ask_user")
	}

	workerReader, err := hoststate.WorkerReaderForSession(t.Context(), askState, "implementer")
	contractcheck.FailErr(t, "WorkerReaderForSession Ask implementer", err)
	if workerReader.Held()[toolcontract.ReachCommandMutation] {
		t.Fatal("Ask-spawned worker reader retained command_mutation")
	}

	roProfile := surface.ResolveTurnProfileForSurface("implement_routing", api.CoordinatorRunContext{}, &api.Session{
		Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
	})
	roPlan, err := surface.CompileToolPlan(roProfile, 1)
	contractcheck.FailErr(t, "CompileToolPlan implement_routing", err)
	roAuth, err := hoststate.FreezeTurnAuthority(t.Context(), hoststate.CoordinatorState{
		Overlay:        stock,
		SessionMode:    api.SessionModeAgent,
		Posture:        api.SessionPostureBuild,
		RootCount:      1,
		HasGit:         true,
		WebSearch:      true,
		TurnProfile:    roProfile,
		Plan:           roPlan,
		SpawnAllowlist: stock.SpawnAllowlist,
	})
	contractcheck.FailErr(t, "FreezeTurnAuthority implement_routing", err)
	cmdProps, _ := roAuth.Schemas["command"]["properties"].(map[string]any)
	if _, hasVerif := cmdProps["verification"]; hasVerif {
		t.Fatal("read-only command schema retained verification property")
	}
}

func TestCoverageTrackerFlagsDeadAndVacuousBranches(t *testing.T) {
	contractcheck.FailErr(t, "install anchor catalog", anchorcatalog.InstallBundled())
	contractcheck.FailErr(t, "install engine declaration", oar.InstallEngineDeclarationBundled(""))

	tracker := hoststate.NewCoverageTracker()
	restore := tracker.Install()
	t.Cleanup(restore)

	tplSource := `{% if active %}yes{% else %}no{% endif %}{% if never_reached %}{% if nested_dead %}x{% endif %}{% endif %}`
	contractcheck.FailErr(t, "RegisterTemplate", tracker.RegisterTemplate("test/sample.md", tplSource, pongoplain.Isolated))
	tpl, err := pongoplain.Compile(tplSource)
	contractcheck.FailErr(t, "Compile", err)

	_, err = pongoplain.Execute(t.Context(), tpl, map[string]any{"active": true, "never_reached": false})
	contractcheck.FailErr(t, "Execute 1", err)
	_, err = pongoplain.Execute(t.Context(), tpl, map[string]any{"active": false, "never_reached": false})
	contractcheck.FailErr(t, "Execute 2", err)

	rule := &oar.Rule{
		ID:     "COV_RULE",
		Kind:   oar.KindPolicy,
		Anchor: oar.AnchorToolPreInvoke,
		When:   `tool == "command"`,
		Effect: oar.EffectBlock,
		NextSteps: &oar.NextSteps{
			Steps: []oar.NextStep{{
				Tool:      "recall",
				Args:      map[string]any{"query": "x"},
				Say:       "Call {{ call }}.",
				Otherwise: "Fallback.",
			}},
		},
	}
	tracker.RegisterRule(rule)
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)

	gc := oar.NewGuardContext()
	gc.Invocation.Tool = "command"
	gc.Reach = toolreach.Offered([]string{"recall"})
	_, err = pipeline.EvaluateBlock(t.Context(), oar.AnchorToolPreInvoke, gc)
	contractcheck.FailErr(t, "EvaluateBlock", err)
	_ = oar.ResolveNextSteps(rule, gc)

	tracker.RecordCell(false, "")
	tracker.RecordCell(true, "requires_agent_surface_rejected_in_ask_mode")

	report := tracker.Report()
	if report.ReachedCells != 1 || report.ExcludedCells != 1 {
		t.Fatalf("unexpected cell counts: reached=%d excluded=%d", report.ReachedCells, report.ExcludedCells)
	}
	if len(report.DeadTemplateBranches()) != 1 {
		t.Fatalf("DeadTemplateBranches = %+v, want 1 (nested_dead)", report.DeadTemplateBranches())
	}
	if len(report.VacuousTemplateBranches()) != 1 {
		t.Fatalf("VacuousTemplateBranches = %+v, want 1 (never_reached)", report.VacuousTemplateBranches())
	}
	if len(report.VacuousRuleBranches()) != 2 {
		t.Fatalf("VacuousRuleBranches = %+v, want 2 (when and steps[0].reach)", report.VacuousRuleBranches())
	}
	if err := report.Validate(); err == nil {
		t.Fatal("expected Validate() error on dead and vacuous branches")
	}
}
