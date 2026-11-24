package hoststate_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
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
	if len(lang.Commands) == 0 || len(lang.Commands) != len(lang.Language.RequiredCommands) {
		t.Fatalf("len(ReadOnlyLanguageV1().Commands) = %d, want non-empty match with RequiredCommands (%d)", len(lang.Commands), len(lang.Language.RequiredCommands))
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
	host := prompts.HostFacts{
		Profiles:              profiles,
		Schemas:               stock.Schemas,
		RootCount:             1,
		HasGit:                true,
		HasAnyGitRoot:         true,
		WebSearch:             true,
		CommandExternalHidden: externalHidden,
	}
	turn := prompts.TurnProjection{ProfileID: profile.ID, Declared: true}
	if surfaceID != "" {
		plan, err := prompts.CompileSurfacePlan(surfaceID, 1)
		contractcheck.FailErr(t, "CompileSurfacePlan "+surfaceID, err)
		host.SpawnAllowlist = stock.SpawnAllowlist
		turn = prompts.TurnProjection{ProfileID: profile.ID, SurfaceID: surfaceID, Plan: plan.Promote(plan.DeferredNames()...), Offered: plan.AddressableNames()}
	} else {
		var offered []string
		for name := range profile.Tools {
			if !strings.HasSuffix(name, "*") {
				offered = append(offered, name)
			}
		}
		turn.Offered = offered
	}
	reader, err := prompts.NewReader(host, turn)
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
		if !coordAuth.Reader.CanReachProgram("nohup") || !coordAuth.ProgramReach("nohup") || coordAuth.Reader.Facts()["profile_command_external_exec"] != true {
			t.Fatalf("coordinator externalHidden=%t: nohup/external_exec became unreachable (Reader=%t ProgramReach=%t ExternalExec=%v)",
				externalHidden, coordAuth.Reader.CanReachProgram("nohup"), coordAuth.ProgramReach("nohup"), coordAuth.Reader.Facts()["profile_command_external_exec"])
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
		if !workerAuth.Reader.CanReachProgram("nohup") || !workerAuth.ProgramReach("nohup") || workerAuth.Reader.Facts()["profile_command_external_exec"] != true {
			t.Fatalf("worker externalHidden=%t: nohup/external_exec became unreachable (Reader=%t ProgramReach=%t ExternalExec=%v)",
				externalHidden, workerAuth.Reader.CanReachProgram("nohup"), workerAuth.ProgramReach("nohup"), workerAuth.Reader.Facts()["profile_command_external_exec"])
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

func expectedToolsAcceptingField(offered []string, schemas map[string]map[string]any, fieldPath string, runnerOnly bool) []string {
	var out []string
	for _, name := range offered {
		if runnerOnly {
			if mr, ok := toolcontract.MultiRootOf(name); !ok || mr != toolcontract.MultiRootCommand {
				continue
			}
		}
		if toolschema.SchemaAcceptsField(schemas[name], fieldPath) {
			out = append(out, name)
		}
	}
	return out
}

func assertReaderRunnerSchemaParity(t *testing.T, label string, reader prompts.Reader, offered []string, reqSchemas map[string]map[string]any) {
	t.Helper()
	facts := reader.Facts()
	checks := []struct {
		factKey    string
		fieldPath  string
		runnerOnly bool
	}{
		{"runner_capability_request_tools", "capability_request", true},
		{"runner_host_resources_tools", "capability_request.host_resources", false},
		{"runner_secret_use_tools", "secret_use", false},
		{"runner_socks_proxy_tools", "socks_proxy", false},
	}
	for _, chk := range checks {
		got, _ := facts[chk.factKey].([]string)
		want := expectedToolsAcceptingField(offered, reqSchemas, chk.fieldPath, chk.runnerOnly)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: %s = %v, want request-schema parity %v (field %q)", label, chk.factKey, got, want, chk.fieldPath)
		}
	}
	gotAskSecret, _ := facts["can_ask_user_secret"].(bool)
	wantAskSecret := slices.Contains(offered, "ask_user") &&
		toolschema.SchemaAcceptsField(reqSchemas["ask_user"], "secret") &&
		toolschema.SchemaAcceptsEnumValue(reqSchemas["ask_user"], "response_type", "secret")
	if gotAskSecret != wantAskSecret {
		t.Fatalf("%s: can_ask_user_secret = %v, want %v", label, gotAskSecret, wantAskSecret)
	}
}

func TestProjectedSchemaToolsParityAcrossNarrowingAndPartialAvailability(t *testing.T) {
	overlays, err := hoststate.DiscoverPackOverlays(t.Context())
	contractcheck.FailErr(t, "DiscoverPackOverlays", err)
	stock := overlays[0]

	t.Run("coordinator_and_worker_request_schema_parity_across_independent_narrowing_axes", func(t *testing.T) {
		type coordCase struct {
			surfID string
			runCtx api.CoordinatorRunContext
		}
		for _, posture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureVet, api.SessionPostureBuild, api.SessionPostureOrchestrate} {
			narrowed, nErr := hoststate.NarrowProfilesForPosture(stock.Profiles, posture, false)
			contractcheck.FailErr(t, "NarrowProfilesForPosture", nErr)
			for _, mode := range []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk} {
				for _, externalHidden := range []bool{false, true} {
					cases := []coordCase{
						{surfID: "implement_investigate"},
						{surfID: "implement_routing"},
						{surfID: "implement_dispatch"},
					}
					if mode == api.SessionModeAsk {
						cases = []coordCase{
							{surfID: "observe_investigate"},
							{
								surfID: "observe_investigate",
								runCtx: api.CoordinatorRunContext{
									RunID:                   "ask-wf-run",
									WorkflowID:              "ask-wf",
									PhaseCoordinatorSurface: "implement_routing",
								},
							},
						}
					}
					for _, cc := range cases {
						tp := surface.ResolveTurnProfileForSurface(cc.surfID, cc.runCtx, &api.Session{Mode: mode, Posture: posture})
						pl, pErr := surface.CompileToolPlan(tp, 1)
						contractcheck.FailErr(t, "CompileToolPlan", pErr)
						for _, loadDeferred := range []bool{false, true} {
							loaded := map[string]bool{}
							if loadDeferred {
								for _, d := range pl.DeferredNames() {
									loaded[d] = true
								}
							}
							auth, fErr := hoststate.FreezeTurnAuthority(t.Context(), hoststate.CoordinatorState{
								Overlay: stock, SessionMode: mode, Posture: posture, RootCount: 1,
								HasGit: true, WebSearch: true, CommandExternalHidden: externalHidden,
								RunContext: cc.runCtx, TurnProfile: tp, Plan: pl, Loaded: loaded,
								SpawnAllowlist: stock.SpawnAllowlist,
							})
							contractcheck.FailErr(t, "FreezeTurnAuthority", fErr)
							lbl := string(posture) + "/" + string(mode) + "/" + cc.surfID + "/" + cc.runCtx.RunID
							assertReaderRunnerSchemaParity(t, "coord-live/"+lbl, auth.Reader, auth.Offered, auth.Schemas)

							derivedReader, dErr := prompts.NewReader(prompts.HostFacts{
								SessionMode:           string(mode),
								Profiles:              narrowed,
								Schemas:               stock.Schemas,
								RootCount:             1,
								HasGit:                true,
								WebSearch:             true,
								CommandExternalHidden: externalHidden,
							}, prompts.TurnProjection{
								ProfileID: prompts.CoordinatorProfileID,
								SurfaceID: cc.surfID,
								Plan:      readerLoadedPlan(auth.Plan, loaded),
								Offered:   readerLoadedOffer(auth.Plan, auth.Offered, loaded),
							})
							contractcheck.FailErr(t, "derived coordinator NewReader", dErr)
							assertReaderRunnerSchemaParity(t, "coord-derived/"+lbl, derivedReader, auth.Offered, auth.Schemas)
						}
					}
				}
			}
		}

		for _, agent := range stock.WorkerAgents {
			for _, posture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureBuild} {
				for _, parentMode := range []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk} {
					narrowed, nErr := hoststate.NarrowProfilesForPosture(stock.Profiles, posture, parentMode == api.SessionModeAsk)
					contractcheck.FailErr(t, "NarrowProfilesForPosture worker", nErr)
					for _, loadExtra := range []bool{false, true} {
						loaded := map[string]bool{}
						if loadExtra {
							loaded["terminal_send"] = true
							loaded["page_open"] = true
						}
						auth, wErr := hoststate.FreezeWorkerAuthority(t.Context(), hoststate.WorkerState{
							Overlay: stock, AgentID: agent.ID, ToolProfile: agent.ToolProfile,
							Posture: posture, ParentSessionMode: parentMode, RootCount: 1,
							HasGit: true, WebSearch: true, Loaded: loaded,
						})
						contractcheck.FailErr(t, "FreezeWorkerAuthority", wErr)
						lbl := agent.ID + "/" + string(posture) + "/" + string(parentMode)
						assertReaderRunnerSchemaParity(t, "worker-live/"+lbl, auth.Reader, auth.Offered, auth.Schemas)

						derivedWorker, dErr := prompts.NewReader(prompts.HostFacts{
							SessionMode: string(parentMode),
							Profiles:    narrowed,
							Schemas:     stock.Schemas,
							RootCount:   1,
							HasGit:      true,
							WebSearch:   true,
						}, prompts.TurnProjection{
							ProfileID: agent.ToolProfile,
							Plan:      readerLoadedPlan(auth.Plan, loaded),
							Offered:   readerLoadedOffer(auth.Plan, auth.Offered, loaded),
						})
						contractcheck.FailErr(t, "derived worker NewReader", dErr)
						assertReaderRunnerSchemaParity(t, "worker-derived/"+lbl, derivedWorker, auth.Offered, auth.Schemas)
					}
				}
			}
		}
	})

	t.Run("partial_availability_subset_witnesses", func(t *testing.T) {
		specAuth, err := hoststate.FreezeWorkerAuthority(t.Context(), hoststate.WorkerState{
			Overlay:           stock,
			AgentID:           "implementer",
			ToolProfile:       "implement",
			Posture:           api.SessionPostureSpec,
			ParentSessionMode: api.SessionModeAgent,
			RootCount:         1,
			HasGit:            true,
			WebSearch:         true,
			Loaded:            map[string]bool{"terminal_send": true, "page_open": true},
		})
		contractcheck.FailErr(t, "FreezeWorkerAuthority spec implementer", err)
		for _, mustOffer := range []string{"command", "terminal_open", "terminal_send", "verify", "page_open", "wait"} {
			if !slices.Contains(specAuth.Offered, mustOffer) {
				t.Fatalf("expected %q to remain offered in partial-availability state; offered=%v", mustOffer, specAuth.Offered)
			}
		}
		facts := specAuth.Reader.Facts()
		wantHostAndSocks := []string{"terminal_open", "verify"}
		wantSecretUse := []string{"terminal_open", "terminal_send", "verify"}
		if got, _ := facts["runner_host_resources_tools"].([]string); !slices.Equal(got, wantHostAndSocks) {
			t.Fatalf("runner_host_resources_tools = %v, want %v (command pruned, page_open/wait lack host_resources)", got, wantHostAndSocks)
		}
		if got, _ := facts["runner_socks_proxy_tools"].([]string); !slices.Equal(got, wantHostAndSocks) {
			t.Fatalf("runner_socks_proxy_tools = %v, want %v", got, wantHostAndSocks)
		}
		if got, _ := facts["runner_secret_use_tools"].([]string); !slices.Equal(got, wantSecretUse) {
			t.Fatalf("runner_secret_use_tools = %v, want %v", got, wantSecretUse)
		}
		if got, _ := facts["runner_capability_request_tools"].([]string); !slices.Equal(got, wantHostAndSocks) {
			t.Fatalf("runner_capability_request_tools = %v, want %v", got, wantHostAndSocks)
		}

		// command accepts host_resources only; verify accepts socks_proxy only.
		customProjected := map[string]map[string]any{
			"command": {
				"type": "object",
				"properties": map[string]any{
					"capability_request": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"host_resources": map[string]any{"type": "array"},
						},
					},
				},
			},
			"verify": {
				"type": "object",
				"properties": map[string]any{
					"socks_proxy": map[string]any{"type": "boolean"},
				},
			},
		}
		asymReader, err := prompts.NewReader(prompts.HostFacts{
			Profiles: []sandbox.ToolProfile{{
				ID:             "asym_runners",
				Tools:          map[string]bool{"command": true, "verify": true},
				CommandEffects: toolcontract.AllCommandEffects(),
			}},
			Schemas:   stock.Schemas,
			RootCount: 1,
		}, prompts.TurnProjection{
			ProfileID:        "asym_runners",
			Plan:             toolsurface.Compile([]string{"command", "verify"}, nil),
			Offered:          []string{"command", "verify"},
			ProjectedSchemas: customProjected,
		})
		contractcheck.FailErr(t, "asymReader", err)
		af := asymReader.Facts()
		if got, _ := af["runner_host_resources_tools"].([]string); !slices.Equal(got, []string{"command"}) {
			t.Fatalf("asym runner_host_resources_tools = %v, want [command]", got)
		}
		if got, _ := af["runner_socks_proxy_tools"].([]string); !slices.Equal(got, []string{"verify"}) {
			t.Fatalf("asym runner_socks_proxy_tools = %v, want [verify]", got)
		}
	})

	t.Run("explicit_frozen_projection_missing_entry_fails_closed", func(t *testing.T) {
		termMeta, ok := stock.Schemas.ToolMeta("terminal_open")
		if !ok {
			t.Fatal("missing terminal_open catalog schema")
		}
		offered := []string{"ask_user", "command", "terminal_open"}
		profile := sandbox.ToolProfile{
			ID:             "missing_entry_probe",
			Tools:          map[string]bool{"ask_user": true, "command": true, "terminal_open": true},
			CommandEffects: toolcontract.AllCommandEffects(),
		}
		baseHost := prompts.HostFacts{
			SessionMode: string(api.SessionModeAgent),
			Profiles:    []sandbox.ToolProfile{profile},
			Schemas:     stock.Schemas,
			RootCount:   1,
		}
		baseTurn := prompts.TurnProjection{
			ProfileID: "missing_entry_probe",
			Plan:      toolsurface.Compile(offered, nil),
			Offered:   offered,
		}

		preFreeze, err := prompts.NewReader(baseHost, baseTurn)
		contractcheck.FailErr(t, "preFreeze NewReader", err)
		pfFacts := preFreeze.Facts()
		if got, _ := pfFacts["runner_host_resources_tools"].([]string); !slices.Equal(got, []string{"command", "terminal_open"}) {
			t.Fatalf("pre-freeze runner_host_resources_tools = %v, want [command terminal_open]", got)
		}
		if got, _ := pfFacts["runner_direct_ip_tools"].([]string); !slices.Equal(got, []string{"command"}) {
			t.Fatalf("pre-freeze runner_direct_ip_tools = %v, want [command]", got)
		}
		if got, _ := pfFacts["can_ask_user_secret"].(bool); !got {
			t.Fatal("pre-freeze can_ask_user_secret = false, want true")
		}

		// Frozen map omits command and ask_user while keeping stock.Schemas populated.
		partialTurn := baseTurn
		partialTurn.ProjectedSchemas = map[string]map[string]any{
			"terminal_open": termMeta.ArgsSchema,
		}
		missingEntryReader, err := prompts.NewReader(baseHost, partialTurn)
		contractcheck.FailErr(t, "missingEntryReader", err)
		if !slices.Equal(missingEntryReader.Offered(), offered) {
			t.Fatalf("offered = %v, want %v", missingEntryReader.Offered(), offered)
		}
		mf := missingEntryReader.Facts()
		for _, onlyTermKey := range []string{
			"runner_capability_request_tools",
			"runner_host_resources_tools",
			"runner_secret_use_tools",
			"runner_socks_proxy_tools",
			"runner_local_listen_tools",
			"runner_loopback_connect_tools",
			"runner_socket_tools",
			"runner_write_root_tools",
		} {
			if got, _ := mf[onlyTermKey].([]string); !slices.Equal(got, []string{"terminal_open"}) {
				t.Fatalf("missing-entry %s = %v, want [terminal_open] (command must not fall back to raw catalog)", onlyTermKey, got)
			}
		}
		if got, _ := mf["runner_direct_ip_tools"].([]string); len(got) != 0 {
			t.Fatalf("missing-entry runner_direct_ip_tools = %v, want empty", got)
		}
		if got, _ := mf["can_ask_user_secret"].(bool); got {
			t.Fatal("missing-entry can_ask_user_secret = true, want false when ask_user has no frozen entry")
		}

		emptyTurn := baseTurn
		emptyTurn.ProjectedSchemas = map[string]map[string]any{}
		emptyFrozenReader, err := prompts.NewReader(baseHost, emptyTurn)
		contractcheck.FailErr(t, "emptyFrozenReader", err)
		ef := emptyFrozenReader.Facts()
		for _, runnerKey := range []string{
			"runner_capability_request_tools",
			"runner_host_resources_tools",
			"runner_secret_use_tools",
			"runner_socks_proxy_tools",
			"runner_direct_ip_tools",
			"runner_local_listen_tools",
			"runner_loopback_connect_tools",
			"runner_socket_tools",
			"runner_write_root_tools",
		} {
			if got, _ := ef[runnerKey].([]string); len(got) != 0 {
				t.Fatalf("empty frozen %s = %v, want empty", runnerKey, got)
			}
		}
		if got, _ := ef["can_ask_user_secret"].(bool); got {
			t.Fatal("empty frozen can_ask_user_secret = true, want false")
		}
		if preFreeze.Fingerprint() == missingEntryReader.Fingerprint() ||
			preFreeze.Fingerprint() == emptyFrozenReader.Fingerprint() ||
			missingEntryReader.Fingerprint() == emptyFrozenReader.Fingerprint() {
			t.Fatalf("expected distinct fingerprints for nil (%s), missing-entry (%s), and empty non-nil (%s)",
				preFreeze.Fingerprint(), missingEntryReader.Fingerprint(), emptyFrozenReader.Fingerprint())
		}
	})

	t.Run("ask_user_enum_narrowing_with_secret_object_present_and_render_cache_distinction", func(t *testing.T) {
		loadedSkills, _ := extpacks.LoadEffectiveSkills(stock.Catalog)
		var secretSkillBody string
		for _, sk := range loadedSkills {
			if sk.Name == "use-secrets-without-reading-them" {
				secretSkillBody = sk.Body
				break
			}
		}
		if secretSkillBody == "" {
			t.Fatal("use-secrets-without-reading-them missing from stock.Skills")
		}
		tpl, err := pongoplain.Compile(secretSkillBody)
		contractcheck.FailErr(t, "Compile use-secrets-without-reading-them", err)

		makeAskSchema := func(enumVals []any) map[string]any {
			return map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt":        map[string]any{"type": "string"},
					"response_type": map[string]any{"type": "string", "enum": enumVals},
					"secret": map[string]any{
						"type":       "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
					},
				},
			}
		}
		offered := []string{"ask_user", "secret_generate", "secret_list", "secret_revoke"}
		prof := sandbox.ToolProfile{
			ID:    "ask_enum_probe",
			Tools: map[string]bool{"ask_user": true, "secret_generate": true, "secret_list": true, "secret_revoke": true},
		}
		buildState := func(enumVals []any) (prompts.Reader, map[string]map[string]any) {
			schemas := map[string]map[string]any{
				"ask_user":        makeAskSchema(enumVals),
				"secret_generate": {"type": "object"},
				"secret_list":     {"type": "object"},
				"secret_revoke":   {"type": "object"},
			}
			r, rErr := prompts.NewReader(prompts.HostFacts{
				Profiles:  []sandbox.ToolProfile{prof},
				Schemas:   stock.Schemas,
				RootCount: 1,
			}, prompts.TurnProjection{
				ProfileID:        "ask_enum_probe",
				Plan:             toolsurface.Compile(offered, nil),
				Offered:          offered,
				ProjectedSchemas: schemas,
			})
			contractcheck.FailErr(t, "NewReader ask_enum_probe", rErr)
			return r, schemas
		}

		admittedReader, admittedSchemas := buildState([]any{"text", "single_choice", "multi_choice", "secret"})
		narrowedReader, narrowedSchemas := buildState([]any{"text", "single_choice", "multi_choice"})

		if admittedReader.Fingerprint() == narrowedReader.Fingerprint() {
			t.Fatalf("Reader.Fingerprint() collided when only response_type.enum narrowed: %s", admittedReader.Fingerprint())
		}
		admittedDigest := hoststate.AuthorityDigest(offered, admittedSchemas, nil, admittedReader.Mints(), admittedReader)
		narrowedDigest := hoststate.AuthorityDigest(offered, narrowedSchemas, nil, narrowedReader.Mints(), narrowedReader)
		if admittedDigest == "" || narrowedDigest == "" || admittedDigest == narrowedDigest {
			t.Fatalf("AuthorityDigest collided or empty: %q vs %q", admittedDigest, narrowedDigest)
		}

		admittedVars, err := prompts.SkillVars(admittedReader)
		contractcheck.FailErr(t, "SkillVars admitted", err)
		admittedVars["configuration"] = map[string]any{}
		narrowedVars, err := prompts.SkillVars(narrowedReader)
		contractcheck.FailErr(t, "SkillVars narrowed", err)
		narrowedVars["configuration"] = map[string]any{}

		cache := hoststate.NewRenderCache()
		keyAdmitted, err := hoststate.TryRenderCacheKey("skill:use-secrets-without-reading-them", stock.Catalog.Revision, nil, admittedVars)
		contractcheck.FailErr(t, "TryRenderCacheKey admitted", err)
		keyNarrowed, err := hoststate.TryRenderCacheKey("skill:use-secrets-without-reading-them", stock.Catalog.Revision, nil, narrowedVars)
		contractcheck.FailErr(t, "TryRenderCacheKey narrowed", err)
		if keyAdmitted == keyNarrowed {
			t.Fatalf("RenderCacheKey collided across enum narrowing: %s", keyAdmitted)
		}

		outAdmitted, hit1, err := cache.RenderOrReuse(keyAdmitted, func() (string, error) {
			return pongoplain.Execute(t.Context(), tpl, admittedVars)
		})
		contractcheck.FailErr(t, "RenderOrReuse admitted", err)
		outNarrowed, hit2, err := cache.RenderOrReuse(keyNarrowed, func() (string, error) {
			return pongoplain.Execute(t.Context(), tpl, narrowedVars)
		})
		contractcheck.FailErr(t, "RenderOrReuse narrowed", err)
		if hit1 || hit2 {
			t.Fatalf("unexpected cache hit across distinct enum states: hit1=%t hit2=%t", hit1, hit2)
		}
		if !strings.Contains(outAdmitted, "call `ask_user` with `response_type: secret` and a `secret` object") || strings.Contains(outAdmitted, "report the need upstream") {
			t.Fatalf("admitted render mismatch:\n%s", outAdmitted)
		}
		if strings.Contains(outNarrowed, "response_type: secret") || !strings.Contains(outNarrowed, "— report the need upstream") {
			t.Fatalf("narrowed render mismatch:\n%s", outNarrowed)
		}
	})
}
