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
					reader := buildWidestTupleReader(t, narrowed, stock, profile, surfaceID)
					held := reader.Held()
					if externalHidden {
						delete(held, toolcontract.ReachCommandExternalExecSelector)
					}

					progReach, reachErr := exec.Metadata.ProgramReach(t.Context(), platform.ToolFilter{
						ProfileID: profile.ID,
						SurfaceID: surfaceID,
					})
					contractcheck.FailErr(t, "Metadata.ProgramReach", reachErr)

					for _, cmd := range lang.Commands {
						got := toolcontract.CommandLanguageReachable(cmd.Name, held)
						want := held["command"] && progReach(cmd.Name)
						if got != want {
							t.Fatalf(
								"posture=%s profile=%s surface=%q externalHidden=%t command=%q: CommandLanguageReachable=%t, Metadata.ProgramReach=%t",
								posture.ID, profile.ID, surfaceID, externalHidden, cmd.Name, got, want,
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
			ProfileID: profile.ID,
			SurfaceID: surfaceID,
			Mode:      surface.ExecutionModeFamily(surfaceID),
			Profiles:  profiles,
			Plan:      plan,
			Loaded:    loaded,
			Schemas:   stock.Schemas,
			RootCount: 1,
			HasGit:    true,
			WebSearch: true,
		}
	} else {
		for name := range profile.DeferredTools {
			if !strings.HasSuffix(name, "*") {
				loaded[name] = true
			}
		}
		planInput = prompts.ReaderInput{
			ProfileID: profile.ID,
			Profiles:  profiles,
			Loaded:    loaded,
			Schemas:   stock.Schemas,
			RootCount: 1,
			HasGit:    true,
			WebSearch: true,
		}
	}
	reader, err := prompts.NewReader(planInput)
	contractcheck.FailErr(t, "prompts.NewReader", err)
	return reader
}

func verifyAuthorityProjections(t *testing.T, stock hoststate.PackOverlay) {
	t.Helper()
	askProfile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{RunID: "run-ask", PhaseCoordinatorSurface: "observe_plan"},
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

	// Ask-spawned implementer worker must be clamped to read-only.
	workerReader, err := hoststate.WorkerReaderForSession(t.Context(), askState, "implementer")
	contractcheck.FailErr(t, "WorkerReaderForSession Ask implementer", err)
	if workerReader.Held()[toolcontract.ReachCommandMutation] {
		t.Fatal("Ask-spawned worker reader retained command_mutation")
	}

	// Read-only surface command schema must prune external-exec fields.
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

func TestRenderCacheKeyDistinguishesNonReaderVars(t *testing.T) {
	readerA, err := prompts.DeclaredReader(nil, "implement_investigate")
	contractcheck.FailErr(t, "DeclaredReader A", err)
	readerB, err := prompts.DeclaredReader(nil, "implement_investigate")
	contractcheck.FailErr(t, "DeclaredReader B", err)
	if readerA.Fingerprint() != readerB.Fingerprint() {
		t.Fatalf("expected matching Reader.Fingerprint(), got %s vs %s", readerA.Fingerprint(), readerB.Fingerprint())
	}

	varsA, err := prompts.Vars(readerA, map[string]any{
		"session_scratch_dir":     "/tmp/scratch-a",
		"reviews_confined_writes": false,
	})
	contractcheck.FailErr(t, "Vars A", err)
	varsB, err := prompts.Vars(readerB, map[string]any{
		"session_scratch_dir":     "/tmp/scratch-b",
		"reviews_confined_writes": true,
	})
	contractcheck.FailErr(t, "Vars B", err)

	keyA := hoststate.RenderCacheKey("partials/command-surface.md", "rev-1", []string{"u1"}, varsA)
	keyB := hoststate.RenderCacheKey("partials/command-surface.md", "rev-1", []string{"u1"}, varsB)
	if keyA == keyB {
		t.Fatal("RenderCacheKey collided across distinct non-Reader variables")
	}

	cache := hoststate.NewRenderCache()
	calls := 0
	out1, hit1, err := cache.RenderOrReuse(keyA, func() (string, error) {
		calls++
		return "rendered-body", nil
	})
	contractcheck.FailErr(t, "RenderOrReuse miss", err)
	out2, hit2, err := cache.RenderOrReuse(keyA, func() (string, error) {
		calls++
		return "different", nil
	})
	contractcheck.FailErr(t, "RenderOrReuse hit", err)
	if hit1 || !hit2 || calls != 1 || out1 != "rendered-body" || out2 != "rendered-body" {
		t.Fatalf("unexpected cache behavior: hit1=%t hit2=%t calls=%d out1=%q out2=%q", hit1, hit2, calls, out1, out2)
	}

	// Same rendered bytes under two different wait schemas must trigger authority recheck.
	auth1 := hoststate.AuthorityDigest(
		[]string{"wait"},
		map[string]map[string]any{"wait": {"enum": []string{"process_done"}}},
		nil,
		readerA.Mints(),
		readerA,
	)
	auth2 := hoststate.AuthorityDigest(
		[]string{"wait"},
		map[string]map[string]any{"wait": {"enum": []string{"next_worker_done"}}},
		nil,
		readerA.Mints(),
		readerA,
	)
	if auth1 == auth2 {
		t.Fatal("AuthorityDigest collided across distinct wait schemas")
	}
	if !cache.NeedsAuthorityCheck(keyA, auth1) {
		t.Fatal("first authority check should be required")
	}
	if cache.NeedsAuthorityCheck(keyA, auth1) {
		t.Fatal("duplicate authority check should be skipped")
	}
	if !cache.NeedsAuthorityCheck(keyA, auth2) {
		t.Fatal("distinct AuthorityDigest on same RenderCacheKey must re-run authority check")
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
