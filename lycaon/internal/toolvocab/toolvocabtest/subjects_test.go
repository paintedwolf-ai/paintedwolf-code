package toolvocabtest_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestClaimSubjectsEvaluateAgainstTargetActorReader(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog", err)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "schemas", err)
	catalog, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	testutil.FailErr(t, "toolvocab catalog", err)
	prunable, err := toolvocabtest.DiscoverPrunableSchemaIndex(t.Context(), eff)
	testutil.FailErr(t, "prunable index", err)
	vocab, err := toolvocabtest.NewBacktickVocab(eff)
	testutil.FailErr(t, "backtick vocab", err)

	invPlan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_investigate"}, 1)
	testutil.FailErr(t, "compile implement_investigate plan", err)
	coordFloorReader, err := prompts.NewReader(prompts.HostFacts{
		SessionMode:    string(api.SessionModeAgent),
		Profiles:       profiles,
		Schemas:        schemas,
		RootCount:      1,
		HasGit:         true,
		HasAnyGitRoot:  true,
		WebSearch:      true,
		SpawnAllowlist: spawn.AmbientAllowedAgents(),
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Plan:      invPlan,
		Offered:   (invPlan).ImmediateNames(),
	})
	testutil.FailErr(t, "coord floor reader", err)

	askProfile := surface.ResolveTurnProfile(api.CoordinatorRunContext{}, &api.Session{Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild}, nil)
	askPlan, err := surface.CompileToolPlan(askProfile, 1)
	testutil.FailErr(t, "compile ask plan", err)
	coordAskReader, err := prompts.NewReader(prompts.HostFacts{
		SessionMode:    string(api.SessionModeAsk),
		Profiles:       profiles,
		Schemas:        schemas,
		RootCount:      1,
		HasGit:         true,
		HasAnyGitRoot:  true,
		WebSearch:      true,
		SpawnAllowlist: prompts.ReadOnlyAgents(spawn.AmbientAllowedAgents()),
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: askProfile.SurfaceID,
		Plan:      askPlan,
		Offered:   (askPlan).ImmediateNames(),
	})
	testutil.FailErr(t, "coord ask reader", err)

	baseCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog:     catalog,
		Prunable:    prunable,
		Vocab:       vocab,
		Self:        toolvocabtest.AuthorityFromReaderAndMetas(coordFloorReader, nil, nil),
		SessionMode: api.SessionModeAgent,
		Posture:     api.SessionPostureBuild,
	}

	t.Run("request_tools calls name requestable tools on an offered loader", func(t *testing.T) {
		valid := toolvocabtest.EvaluateClaimBlock(baseCtx, toolvocabtest.ClaimBlock{
			Label:   "valid-loader",
			Subject: toolvocabtest.SubjectSelfNow(),
			Text:    "Load verification with `request_tools(need=\"verify\")`.",
		})
		if len(valid) > 0 {
			t.Fatalf("expected valid request_tools(need=\"verify\") to pass, got %+v", valid)
		}

		invalid := toolvocabtest.EvaluateClaimBlock(baseCtx, toolvocabtest.ClaimBlock{
			Label:   "invalid-edit-loader",
			Subject: toolvocabtest.SubjectSelfNow(),
			Text:    "Load editing via `request_tools(need=\"edit\")`.",
		})
		if len(invalid) == 0 {
			t.Fatal("expected request_tools(need=\"edit\") to fail when edit is not in Requestable()")
		}

		noLoaderAuth := toolvocabtest.AuthorityFromReaderAndMetas(coordAskReader, nil, nil)
		noLoaderCtx := baseCtx
		noLoaderCtx.Self = noLoaderAuth
		if !slicesContains(coordAskReader.Offered(), "request_tools") {
			unoffered := toolvocabtest.EvaluateClaimBlock(noLoaderCtx, toolvocabtest.ClaimBlock{
				Label:   "loader-unoffered",
				Subject: toolvocabtest.SubjectSelfNow(),
				Text:    "Call `request_tools(need=\"verify\")`.",
			})
			if len(unoffered) == 0 {
				t.Fatal("expected a request_tools call to fail when request_tools is not offered")
			}
		}
	})

	t.Run("SubjectNamedAgent checks target worker Reader under Agent vs Ask mode", func(t *testing.T) {
		implAgentPass := toolvocabtest.EvaluateClaimBlock(baseCtx, toolvocabtest.ClaimBlock{
			Label:   "roster-implementer-agent",
			Subject: toolvocabtest.SubjectNamedAgent("implementer"),
			Text:    "Product edits only — dispatch with scope.mode write + paths.",
		})
		if len(implAgentPass) > 0 {
			t.Fatalf("expected implementer roster description to pass in Agent build mode, got %+v", implAgentPass)
		}

		askCtx := baseCtx
		askCtx.SessionMode = api.SessionModeAsk
		askCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(coordAskReader, nil, nil)

		implAskFail := toolvocabtest.EvaluateClaimBlock(askCtx, toolvocabtest.ClaimBlock{
			Label:   "roster-implementer-ask",
			Subject: toolvocabtest.SubjectNamedAgent("implementer"),
			Text:    "Product edits only — dispatch with scope.mode write + paths.",
		})
		if len(implAskFail) == 0 {
			t.Fatal("expected mutating implementer roster entry in Ask mode to fail")
		}

		roUnreachable := toolvocabtest.EvaluateClaimBlock(askCtx, toolvocabtest.ClaimBlock{
			Label:   "roster-path-explorer-unreachable",
			Subject: toolvocabtest.SubjectNamedAgent("path-explorer"),
			Text:    "Lands results with `promote_overlay`.",
		})
		if !hasViolationClaim(roUnreachable, "promote_overlay") {
			t.Fatalf("expected a roster entry naming a tool path-explorer cannot reach to fail, got %+v", roUnreachable)
		}

		roValidPass := toolvocabtest.EvaluateClaimBlock(askCtx, toolvocabtest.ClaimBlock{
			Label:   "roster-path-explorer-read",
			Subject: toolvocabtest.SubjectNamedAgent("path-explorer"),
			Text:    "Bounded path, find, and grep code survey.",
		})
		if len(roValidPass) > 0 {
			t.Fatalf("expected read-only path-explorer description to pass in Ask mode, got %+v", roValidPass)
		}
	})

	testDeferredAndHistoricalSubjects(t, eff, schemas, baseCtx, coordFloorReader, coordAskReader)
}

func testDeferredAndHistoricalSubjects(
	t *testing.T,
	eff *extpacks.EffectiveCatalog,
	schemas *toolschema.Config,
	baseCtx toolvocabtest.ClaimEvaluationContext,
	coordFloorReader, coordAskReader prompts.Reader,
) {
	t.Helper()
	t.Run("SubjectFollowupLeg and SubjectSessionNextTurn evaluate non-closeout Reader", func(t *testing.T) {
		engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Catalog: eff})
		closeoutOnlyProfile := []sandbox.ToolProfile{{ID: "closeout_only", Tools: map[string]bool{"complete_leg": true}}}
		closeoutSelfReader, err := prompts.NewReader(prompts.HostFacts{
			Profiles:  closeoutOnlyProfile,
			Schemas:   schemas,
			RootCount: 1,
		}, prompts.TurnProjection{
			ProfileID: "closeout_only",
			Declared:  true,
		})
		testutil.FailErr(t, "closeout self reader", err)

		workerCloseout, rErr := engine.RenderKick(t.Context(), closeoutSelfReader, "worker-closeout", map[string]any{
			"reason_text":                  "step budget reached",
			"llm_timeout":                  true,
			"leg_command_mutation_capable": true,
		})
		testutil.FailErr(t, "render worker-closeout", rErr)

		implAuth, err := toolvocabtest.ResolveWorkerAuthorityForSession(t.Context(), eff, api.SessionModeAgent, api.SessionPostureBuild, 1, true, true, true, false, "implementer")
		testutil.FailErr(t, "resolve implementer auth", err)
		roWorkerAuth, err := toolvocabtest.ResolveWorkerAuthorityForSession(t.Context(), eff, api.SessionModeAsk, api.SessionPostureBuild, 1, true, false, true, false, "path-explorer")
		testutil.FailErr(t, "resolve path-explorer auth", err)

		mutLegCtx := baseCtx
		mutLegCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(closeoutSelfReader, nil, nil)
		mutLegCtx.FollowupLeg = &implAuth
		if got := toolvocabtest.EvaluateClaimBlock(mutLegCtx, toolvocabtest.ClaimBlock{
			Label: "worker-closeout-implementer", Subject: toolvocabtest.SubjectFollowupLeg(true), Text: workerCloseout,
		}); len(got) > 0 {
			t.Fatalf("expected worker-closeout follow-up advice to pass for mutating implementer leg, got %+v", got)
		}

		roLegCtx := mutLegCtx
		roLegCtx.SessionMode = api.SessionModeAsk
		roLegCtx.FollowupLeg = &roWorkerAuth
		if got := toolvocabtest.EvaluateClaimBlock(roLegCtx, toolvocabtest.ClaimBlock{
			Label: "worker-closeout-readonly", Subject: toolvocabtest.SubjectFollowupLeg(true), Text: workerCloseout,
		}); len(got) == 0 {
			t.Fatal("expected mutating worker-closeout follow-up advice to fail for read-only worker leg")
		}
		workerCloseoutRO, roErr := engine.RenderKick(t.Context(), closeoutSelfReader, "worker-closeout", map[string]any{
			"reason_text":                  "step budget reached",
			"llm_timeout":                  true,
			"leg_command_mutation_capable": false,
		})
		testutil.FailErr(t, "render worker-closeout readonly", roErr)
		if got := toolvocabtest.EvaluateClaimBlock(roLegCtx, toolvocabtest.ClaimBlock{
			Label: "worker-closeout-readonly-gated", Subject: toolvocabtest.SubjectFollowupLeg(false), Text: workerCloseoutRO,
		}); len(got) > 0 {
			t.Fatalf("expected gated worker-closeout follow-up advice to pass for read-only worker leg, got %+v", got)
		}

		zeroToolProfile := []sandbox.ToolProfile{{ID: "zero_closeout", Tools: map[string]bool{}}}
		zeroSelfReader, err := prompts.NewReader(prompts.HostFacts{
			Profiles:  zeroToolProfile,
			Schemas:   schemas,
			RootCount: 1,
		}, prompts.TurnProjection{
			ProfileID: "zero_closeout",
			Declared:  true,
		})
		testutil.FailErr(t, "zero closeout reader", err)

		coordCloseout, cErr := engine.RenderKick(t.Context(), zeroSelfReader, "coordinator-turn-closeout", map[string]any{
			"reason_text":                      "turn limit reached",
			"llm_timeout":                      true,
			"session_command_mutation_capable": true,
		})
		testutil.FailErr(t, "render coordinator-turn-closeout", cErr)

		mutCoordAuth := toolvocabtest.AuthorityFromReaderAndMetas(coordFloorReader, nil, nil)
		askCoordAuth := toolvocabtest.AuthorityFromReaderAndMetas(coordAskReader, nil, nil)

		nextTurnAgentCtx := baseCtx
		nextTurnAgentCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(zeroSelfReader, nil, nil)
		nextTurnAgentCtx.SessionNextTurn = &mutCoordAuth
		if got := toolvocabtest.EvaluateClaimBlock(nextTurnAgentCtx, toolvocabtest.ClaimBlock{
			Label: "coord-closeout-agent", Subject: toolvocabtest.SubjectSessionNextTurn(true), Text: coordCloseout,
		}); len(got) > 0 {
			t.Fatalf("expected coordinator-turn-closeout next-turn advice to pass in mutating Agent session, got %+v", got)
		}

		nextTurnAskCtx := nextTurnAgentCtx
		nextTurnAskCtx.SessionMode = api.SessionModeAsk
		nextTurnAskCtx.SessionNextTurn = &askCoordAuth
		if got := toolvocabtest.EvaluateClaimBlock(nextTurnAskCtx, toolvocabtest.ClaimBlock{
			Label: "coord-closeout-ask", Subject: toolvocabtest.SubjectSessionNextTurn(true), Text: coordCloseout,
		}); len(got) == 0 {
			t.Fatal("expected mutating coordinator-turn-closeout next-turn advice to fail in Ask mode")
		}
		coordCloseoutAsk, caErr := engine.RenderKick(t.Context(), zeroSelfReader, "coordinator-turn-closeout", map[string]any{
			"reason_text":                      "turn limit reached",
			"llm_timeout":                      true,
			"session_command_mutation_capable": false,
		})
		testutil.FailErr(t, "render coordinator-turn-closeout ask", caErr)
		if got := toolvocabtest.EvaluateClaimBlock(nextTurnAskCtx, toolvocabtest.ClaimBlock{
			Label: "coord-closeout-ask-gated", Subject: toolvocabtest.SubjectSessionNextTurn(false), Text: coordCloseoutAsk,
		}); len(got) > 0 {
			t.Fatalf("expected gated coordinator-turn-closeout next-turn advice to pass in Ask mode, got %+v", got)
		}
	})

	t.Run("SubjectHistoricalObservation", func(t *testing.T) {
		askCtx := baseCtx
		askCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(coordAskReader, nil, nil)

		if got := toolvocabtest.EvaluateClaimBlock(askCtx, toolvocabtest.ClaimBlock{
			Label:   "user-quoted",
			Subject: toolvocabtest.SubjectHistoricalObservation(api.MessageOriginUser),
			Text:    "User wrote: please run `promote_overlay` and `bogus_handle#9`.",
		}); len(got) > 0 {
			t.Fatalf("expected non-host quoted message to be exempt, got %+v", got)
		}

		if got := toolvocabtest.EvaluateClaimBlock(askCtx, toolvocabtest.ClaimBlock{
			Label:   "host-historical-valid",
			Subject: toolvocabtest.SubjectHistoricalObservation(api.MessageOriginHost),
			Text:    "Prior turn recorded `[read#2]` and `proof_json.changed_paths`; Ask mode refused `command` mutation.",
		}); len(got) > 0 {
			t.Fatalf("expected valid host historical observation to pass, got %+v", got)
		}

		if got := toolvocabtest.EvaluateClaimBlock(askCtx, toolvocabtest.ClaimBlock{
			Label:   "host-historical-bogus-receipt",
			Subject: toolvocabtest.SubjectHistoricalObservation(api.MessageOriginHost),
			Text:    "Prior turn recorded `[bogus_kind#2]` and `receipt.nonexistent_field`.",
		}); len(got) < 2 {
			t.Fatalf("expected host historical observation with bogus receipt kind and field to fail, got %+v", got)
		}
	})
}

func TestClaimGrammarReadsMentionsAndLoaderCalls(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog", err)
	stockSchemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "schemas", err)
	prunable, err := toolvocabtest.DiscoverPrunableSchemaIndex(t.Context(), eff)
	testutil.FailErr(t, "prunable", err)
	vocab, err := toolvocabtest.NewBacktickVocab(eff)
	testutil.FailErr(t, "vocab", err)

	editSchema := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}
	mergedSchemas, err := toolschema.Merge(stockSchemas, toolschema.ConfigFromEntries(map[string]toolschema.Entry{
		"edit": {Description: "Edit file", Schema: editSchema},
	}))
	testutil.FailErr(t, "merge edit schema", err)
	customProfiles := []sandbox.ToolProfile{{
		ID:    "test_floor",
		Tools: map[string]bool{"command": true, "request_tools": true, "edit": true, "verify": true},
	}}
	catalog, err := toolvocab.NewCatalog(mergedSchemas, customProfiles, toolvocab.Provenance{})
	testutil.FailErr(t, "catalog with edit", err)

	planWithEditAndVerifyDeferred := toolsurface.Compile([]string{"command", "request_tools"}, []string{"edit", "verify"})
	readerWithEditReq, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  customProfiles,
		Schemas:   mergedSchemas,
		RootCount: 1,
		HasGit:    true,
	}, prompts.TurnProjection{
		ProfileID: "test_floor",
		Plan:      planWithEditAndVerifyDeferred,
		Offered:   (planWithEditAndVerifyDeferred).ImmediateNames(),
	})
	testutil.FailErr(t, "reader with edit requestable", err)

	cmdBase, ok := stockSchemas.ToolMeta("command")
	if !ok {
		t.Fatal("missing stock command schema")
	}
	roCmdSchema := toolschema.PruneCommandReadOnlySchema(cmdBase.ArgsSchema)
	reqMeta, _ := stockSchemas.ToolMeta("request_tools")
	metas := []tools.ToolMeta{
		{Name: "command", ArgsSchema: roCmdSchema},
		{Name: "request_tools", ArgsSchema: reqMeta.ArgsSchema},
	}

	evalCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog:  catalog,
		Prunable: prunable,
		Vocab:    vocab,
		Self: toolvocabtest.AuthorityFromReaderAndMetas(readerWithEditReq, nil, metas,
			toolvocabtest.WithPostLoadAuthority(map[string]map[string]any{"edit": editSchema}, nil)),
		SessionMode: api.SessionModeAgent,
		Posture:     api.SessionPostureBuild,
	}

	eval := func(ctx toolvocabtest.ClaimEvaluationContext, text string) []toolvocabtest.SubjectViolation {
		return toolvocabtest.EvaluateClaimBlock(ctx, toolvocabtest.ClaimBlock{Label: "grammar", Subject: toolvocabtest.SubjectSelfNow(), Text: text})
	}

	t.Run("a mention of an unreachable tool fails whatever its polarity", func(t *testing.T) {
		for _, text := range []string{
			"Call `promote_overlay` now.",
			"Never call `promote_overlay`.",
			"`promote_overlay` is not available on this turn.",
		} {
			if got := eval(evalCtx, text); !hasViolationClaim(got, "promote_overlay") {
				t.Fatalf("%q: want promote_overlay violation, got %+v", text, got)
			}
		}
	})

	t.Run("a mention of an offered or requestable tool passes whatever its polarity", func(t *testing.T) {
		for _, text := range []string{
			"Do not call `edit` on this surface.",
			"Use `edit` once loaded.",
			"Do not repeat `command`.",
		} {
			if got := eval(evalCtx, text); len(got) > 0 {
				t.Fatalf("%q: want no violations, got %+v", text, got)
			}
		}
	})

	t.Run("a call to a requestable tool needs an earlier exact request_tools call in its block", func(t *testing.T) {
		for _, text := range []string{
			"Call `request_tools(need=\"edit\")`, then `edit(path=\"a.go\")`.",
			"1. `request_tools(need=\"edit\")`\n   then `edit(path=\"a.go\")`",
		} {
			if got := eval(evalCtx, text); len(got) > 0 {
				t.Fatalf("%q: want no violations, got %+v", text, got)
			}
		}
		for _, text := range []string{
			"Call `edit(path=\"a.go\")`.",
			"Call `edit(path=\"a.go\")` after `request_tools(need=\"edit\")`.",
			"Call `request_tools(need=\"verify\")`, then `edit(path=\"a.go\")`.",
			"Call `request_tools(need=\"edit file contents\")`, then `edit(path=\"a.go\")`.",
			"Call `request_tools(need=\"edit\")`.\n\nThen `edit(path=\"a.go\")`.",
			"- `request_tools(need=\"edit\")`\n- `edit(path=\"a.go\")`",
		} {
			if got := eval(evalCtx, text); !hasViolationClaim(got, "edit") {
				t.Fatalf("%q: want edit violation, got %+v", text, got)
			}
		}
	})

	t.Run("request_tools must name a requestable tool", func(t *testing.T) {
		planWithoutEdit := toolsurface.Compile([]string{"command", "request_tools"}, []string{"verify"})
		readerWithoutEdit, rErr := prompts.NewReader(prompts.HostFacts{
			Profiles:  customProfiles,
			Schemas:   mergedSchemas,
			RootCount: 1,
		}, prompts.TurnProjection{
			ProfileID: "test_floor",
			Plan:      planWithoutEdit,
			Offered:   (planWithoutEdit).ImmediateNames(),
		})
		testutil.FailErr(t, "reader without edit", rErr)
		noEditCtx := evalCtx
		noEditCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(readerWithoutEdit, nil, metas)
		if got := eval(noEditCtx, "Call `request_tools(need=\"edit\")`."); len(got) == 0 {
			t.Fatal("loading an unrequestable tool passed")
		}
		if got := eval(noEditCtx, "Call `request_tools(need=\"verify\")`."); len(got) > 0 {
			t.Fatalf("loading a requestable tool failed: %+v", got)
		}
	})
}

func TestPostLoadSchemaProjectionAndLoadingOnlyAuthority(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog", err)
	stockSchemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "schemas", err)
	prunable, err := toolvocabtest.DiscoverPrunableSchemaIndex(t.Context(), eff)
	testutil.FailErr(t, "prunable", err)
	vocab, err := toolvocabtest.NewBacktickVocab(eff)
	testutil.FailErr(t, "vocab", err)

	editSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path":  map[string]any{"type": "string"},
			"old_string": map[string]any{"type": "string"},
			"new_string": map[string]any{"type": "string"},
		},
		"required": []any{"file_path", "old_string", "new_string"},
	}
	mergedSchemas, err := toolschema.Merge(stockSchemas, toolschema.ConfigFromEntries(map[string]toolschema.Entry{
		"edit": {Description: "Edit file", Schema: editSchema},
	}))
	testutil.FailErr(t, "merge edit schema", err)
	reqMeta, _ := stockSchemas.ToolMeta("request_tools")
	cmdMeta, _ := stockSchemas.ToolMeta("command")
	reqOnlyMetas := []tools.ToolMeta{{Name: "request_tools", ArgsSchema: reqMeta.ArgsSchema}}

	fullProfiles := []sandbox.ToolProfile{{
		ID:             "postload_full",
		Tools:          map[string]bool{"request_tools": true, "command": true, "edit": true},
		CommandEffects: toolcontract.AllCommandEffects(),
	}}
	roEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "ro effects", err)
	roProfiles := []sandbox.ToolProfile{{
		ID:             "postload_ro",
		Tools:          map[string]bool{"request_tools": true, "command": true, "edit": true},
		CommandEffects: roEffects,
	}}
	catalog, err := toolvocab.NewCatalog(mergedSchemas, fullProfiles, toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	plan := toolsurface.Compile([]string{"request_tools"}, []string{"command", "edit"})

	fullReader, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  fullProfiles,
		Schemas:   mergedSchemas,
		RootCount: 1,
		HasGit:    true,
	}, prompts.TurnProjection{
		ProfileID: "postload_full",
		Plan:      plan,
		Offered:   (plan).ImmediateNames(),
	})
	testutil.FailErr(t, "full reader", err)
	roReader, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  roProfiles,
		Schemas:   mergedSchemas,
		RootCount: 1,
		HasGit:    true,
	}, prompts.TurnProjection{
		ProfileID: "postload_ro",
		Plan:      plan,
		Offered:   (plan).ImmediateNames(),
	})
	testutil.FailErr(t, "ro reader", err)

	fullPostSchemas := map[string]map[string]any{
		"command": toolschema.ProjectArgsSchema(cmdMeta.ArgsSchema),
		"edit":    toolschema.ProjectArgsSchema(editSchema),
	}
	fullAuth := toolvocabtest.AuthorityFromReaderAndMetas(
		fullReader, nil, reqOnlyMetas, toolvocabtest.WithPostLoadAuthority(fullPostSchemas, nil),
	)
	fullCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog: catalog, Prunable: prunable, Vocab: vocab, Self: fullAuth,
		SessionMode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
	}

	for _, okProse := range []string{
		`Load ` + "`edit`" + ` via ` + "`request_tools(need=\"edit\")`" + ` before setting ` + "`edit.file_path`" + `.`,
		`Load ` + "`edit`" + ` via ` + "`request_tools(need=\"edit\")`" + ` before calling ` + "`edit(file_path=\"a.go\", old_string=\"x\", new_string=\"y\")`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before passing ` + "`command.verification`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before calling ` + "`command(command=\"ls\", verification=true)`" + `.`,
	} {
		useCtx := fullCtx
		if okProse == `Load `+"`command`"+` via `+"`request_tools(need=\"command\")`"+` before calling `+"`command(command=\"ls\", verification=true)`"+`.` {
			implAuth, wErr := toolvocabtest.ResolveWorkerAuthorityForSession(t.Context(), eff, api.SessionModeAgent, api.SessionPostureBuild, 1, true, true, true, false, "implementer")
			testutil.FailErr(t, "implementer auth", wErr)
			useCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(
				fullReader, nil, reqOnlyMetas, toolvocabtest.WithPostLoadAuthority(fullPostSchemas, implAuth.PostLoadPrograms),
			)
		}
		if got := toolvocabtest.EvaluateClaimBlock(useCtx, toolvocabtest.ClaimBlock{Label: "postload-admitted", Subject: toolvocabtest.SubjectSelfNow(), Text: okProse}); len(got) > 0 {
			t.Fatalf("expected admitted post-load claim %q to pass, got %+v", okProse, got)
		}
	}

	nonexistent := toolvocabtest.EvaluateClaimBlock(fullCtx, toolvocabtest.ClaimBlock{
		Label: "postload-nonexistent-field", Subject: toolvocabtest.SubjectSelfNow(),
		Text: `Load ` + "`edit`" + ` via ` + "`request_tools(need=\"edit\")`" + ` before setting ` + "`edit.nonexistent_field`" + `.`,
	})
	if !hasViolationClaim(nonexistent, "nonexistent_field") {
		t.Fatalf("expected nonexistent_field violation after edit load, got %+v", nonexistent)
	}

	roPostSchemas := map[string]map[string]any{
		"command": toolschema.PruneCommandReadOnlySchema(cmdMeta.ArgsSchema),
	}
	roCtx := fullCtx
	roCtx.Posture = api.SessionPostureSpec
	roCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(
		roReader, nil, reqOnlyMetas, toolvocabtest.WithPostLoadAuthority(roPostSchemas, nil),
	)
	for _, prunedProse := range []string{
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before passing ` + "`command.verification`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before calling ` + "`command(command=\"ls\", verification=true)`" + `.`,
	} {
		got := toolvocabtest.EvaluateClaimBlock(roCtx, toolvocabtest.ClaimBlock{Label: "postload-pruned", Subject: toolvocabtest.SubjectSelfNow(), Text: prunedProse})
		if !hasViolationClaim(got, "verification") {
			t.Fatalf("expected pruned verification violation for %q, got %+v", prunedProse, got)
		}
	}

	loadOnlyCtx := fullCtx
	loadOnlyCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(fullReader, nil, reqOnlyMetas)
	if got := toolvocabtest.EvaluateClaimBlock(loadOnlyCtx, toolvocabtest.ClaimBlock{
		Label: "load-only-bare", Subject: toolvocabtest.SubjectSelfNow(),
		Text: `Load ` + "`edit`" + ` via ` + "`request_tools(need=\"edit\")`" + `.`,
	}); len(got) > 0 {
		t.Fatalf("expected specific loader instruction to pass under loading-only authority, got %+v", got)
	}
	if got := toolvocabtest.EvaluateClaimBlock(loadOnlyCtx, toolvocabtest.ClaimBlock{
		Label: "load-only-uncertified-field", Subject: toolvocabtest.SubjectSelfNow(),
		Text: `Load ` + "`edit`" + ` via ` + "`request_tools(need=\"edit\")`" + ` before setting ` + "`edit.file_path`" + `.`,
	}); len(got) == 0 {
		t.Fatal("expected unverified post-load edit.file_path claim to fail under loading-only authority")
	}
}

func TestPostLoadCommandModeAdmission(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog", err)
	stockSchemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "schemas", err)
	prunable, err := toolvocabtest.DiscoverPrunableSchemaIndex(t.Context(), eff)
	testutil.FailErr(t, "prunable", err)
	vocab, err := toolvocabtest.NewBacktickVocab(eff)
	testutil.FailErr(t, "vocab", err)

	roWorkerAuth, err := toolvocabtest.ResolveWorkerAuthorityForSession(
		t.Context(), eff, api.SessionModeAgent, api.SessionPostureSpec, 1, true, false, true, false, "path-explorer",
	)
	testutil.FailErr(t, "ro worker auth", err)

	roEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "ro effects", err)
	roProfiles := []sandbox.ToolProfile{{
		ID:             "postload_cmd_ro",
		Tools:          map[string]bool{"request_tools": true, "command": true},
		CommandEffects: roEffects,
	}}
	catalog, err := toolvocab.NewCatalog(stockSchemas, roProfiles, toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	plan := toolsurface.Compile([]string{"request_tools"}, []string{"command"})
	roReader, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  roProfiles,
		Schemas:   stockSchemas,
		RootCount: 1,
		HasGit:    true,
	}, prompts.TurnProjection{
		ProfileID: "postload_cmd_ro",
		Plan:      plan,
		Offered:   (plan).ImmediateNames(),
	})
	testutil.FailErr(t, "ro reader", err)
	reqMeta, _ := stockSchemas.ToolMeta("request_tools")
	cmdMeta, _ := stockSchemas.ToolMeta("command")
	roPostSchemas := map[string]map[string]any{"command": toolschema.PruneCommandReadOnlySchema(cmdMeta.ArgsSchema)}
	roCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog:  catalog,
		Prunable: prunable,
		Vocab:    vocab,
		Self: toolvocabtest.AuthorityFromReaderAndMetas(
			roReader, nil, []tools.ToolMeta{{Name: "request_tools", ArgsSchema: reqMeta.ArgsSchema}},
			toolvocabtest.WithPostLoadAuthority(roPostSchemas, roWorkerAuth.PostLoadPrograms),
		),
		SessionMode: api.SessionModeAgent,
		Posture:     api.SessionPostureSpec,
	}

	for _, admitted := range []string{
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before running ` + "`sed -n 1,5p README.md`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before running ` + "`tar -t -f archive.tar`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before running ` + "`git status`" + `.`,
	} {
		if got := toolvocabtest.EvaluateClaimBlock(roCtx, toolvocabtest.ClaimBlock{Label: "admitted-ro-mode", Subject: toolvocabtest.SubjectSelfNow(), Text: admitted}); len(got) > 0 {
			t.Fatalf("expected admitted read-only command mode %q to pass, got %+v", admitted, got)
		}
	}

	for _, unavailable := range []string{
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before running ` + "`sed 'w out' README.md`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before running ` + "`tar -x -f archive.tar`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before running ` + "`git commit -m msg`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before calling ` + "`command(command=\"sed 'w out' README.md\")`" + `.`,
	} {
		if got := toolvocabtest.EvaluateClaimBlock(roCtx, toolvocabtest.ClaimBlock{Label: "unavailable-mut-mode", Subject: toolvocabtest.SubjectSelfNow(), Text: unavailable}); len(got) == 0 {
			t.Fatalf("expected unavailable mutating command mode %q to fail after read-only command load", unavailable)
		}
	}

	for _, unloaded := range []string{
		"Run `git status`.",
		"Run `git status`, then `request_tools(need=\"command\")`.",
		"`request_tools(need=\"command\")`\n\nRun `git status`.",
	} {
		if got := toolvocabtest.EvaluateClaimBlock(roCtx, toolvocabtest.ClaimBlock{Label: "unloaded-command-span", Subject: toolvocabtest.SubjectSelfNow(), Text: unloaded}); len(got) == 0 {
			t.Fatalf("expected command span %q without an earlier loader in its block to fail", unloaded)
		}
	}
}

func TestDeferredTaskSpawnAllowlistMatchesPostLoadRequestAndNoProjectionFailsClosed(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog", err)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "schemas", err)
	catalog, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	testutil.FailErr(t, "vocab catalog", err)
	prunable, err := toolvocabtest.DiscoverPrunableSchemaIndex(t.Context(), eff)
	testutil.FailErr(t, "prunable", err)
	vocab, err := toolvocabtest.NewBacktickVocab(eff)
	testutil.FailErr(t, "vocab", err)

	spec := toolvocabtest.CoordinatorTurnSpec{
		Catalog:        eff,
		SessionMode:    api.SessionModeAsk,
		Posture:        api.SessionPostureBuild,
		SurfaceID:      "observe_investigate",
		RootCount:      1,
		HasGit:         true,
		WebSearch:      true,
		SpawnAllowlist: spawn.AmbientAllowedAgents(),
	}
	preAuth, err := toolvocabtest.ResolveCoordinatorTurnAuthority(t.Context(), spec)
	testutil.FailErr(t, "pre-load coordinator authority", err)
	if slicesContains(preAuth.Reader.Offered(), "task") || !slicesContains(preAuth.Reader.Requestable(), "task") {
		t.Fatalf("expected task deferred on pre-load observe_investigate Ask turn, offered=%v requestable=%v", preAuth.Reader.Offered(), preAuth.Reader.Requestable())
	}

	postSpec := spec
	postSpec.Loaded = map[string]bool{"task": true}
	postAuth, err := toolvocabtest.ResolveCoordinatorTurnAuthority(t.Context(), postSpec)
	testutil.FailErr(t, "post-load coordinator authority", err)
	if !slicesContains(postAuth.Reader.Offered(), "task") {
		t.Fatalf("expected task callable on post-load request, offered=%v", postAuth.Reader.Offered())
	}
	postTaskSchema := postAuth.Schemas["task"]
	if !reflect.DeepEqual(preAuth.PostLoadSchemas["task"], postTaskSchema) {
		t.Fatalf("pre-load deferred task projection disagreed with actual post-load request schema")
	}
	if !toolschema.SchemaAcceptsEnumValue(postTaskSchema, "agent_type", "path-explorer") || toolschema.SchemaAcceptsEnumValue(postTaskSchema, "agent_type", "implementer") {
		t.Fatalf("expected post-load task schema to admit path-explorer and prune implementer")
	}

	projectedCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog: catalog, Prunable: prunable, Vocab: vocab, Self: preAuth,
		SessionMode: api.SessionModeAsk, Posture: api.SessionPostureBuild,
	}
	admittedProse := `Load ` + "`task`" + ` via ` + "`request_tools(need=\"task\")`" + ` before calling ` + "`task(agent_type=\"path-explorer\", …)`" + `.`
	if got := toolvocabtest.EvaluateClaimBlock(projectedCtx, toolvocabtest.ClaimBlock{
		Label: "deferred-task-admitted-agent", Subject: toolvocabtest.SubjectSelfNow(), Text: admittedProse,
	}); len(got) > 0 {
		t.Fatalf("expected admitted post-load task(agent_type=\"path-explorer\") to pass, got %+v", got)
	}

	for _, prunedTaskProse := range []string{
		`Load ` + "`task`" + ` via ` + "`request_tools(need=\"task\")`" + ` before calling ` + "`task(agent_type=\"implementer\", …)`" + `.`,
		`Load ` + "`task`" + ` via ` + "`request_tools(need=\"task\")`" + ` before setting ` + "`task.agent_type=\"implementer\"`" + `.`,
	} {
		got := toolvocabtest.EvaluateClaimBlock(projectedCtx, toolvocabtest.ClaimBlock{
			Label: "deferred-task-pruned-implementer", Subject: toolvocabtest.SubjectSelfNow(), Text: prunedTaskProse,
		})
		if !hasViolationClaim(got, "agent_type") {
			t.Fatalf("expected pruned implementer agent_type to fail for %q, got %+v", prunedTaskProse, got)
		}
	}

	noProjCtx := projectedCtx
	noProjCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(preAuth.Reader, preAuth.ProgramReach, preAuth.Metas)
	if got := toolvocabtest.EvaluateClaimBlock(noProjCtx, toolvocabtest.ClaimBlock{
		Label: "no-projection-loader-only", Subject: toolvocabtest.SubjectSelfNow(),
		Text: `Load ` + "`task`" + ` via ` + "`request_tools(need=\"task\")`" + `.`,
	}); len(got) > 0 {
		t.Fatalf("expected loader-only instruction to pass without post-load projections, got %+v", got)
	}
	for _, uncertified := range []string{
		admittedProse,
		`Load ` + "`task`" + ` via ` + "`request_tools(need=\"task\")`" + ` before setting ` + "`task.agent_type`" + `.`,
	} {
		if got := toolvocabtest.EvaluateClaimBlock(noProjCtx, toolvocabtest.ClaimBlock{
			Label: "no-projection-uncertified-followup", Subject: toolvocabtest.SubjectSelfNow(), Text: uncertified,
		}); len(got) == 0 {
			t.Fatalf("expected uncertified follow-up %q to fail without post-load projections", uncertified)
		}
	}
}

func hasViolationClaim(violations []toolvocabtest.SubjectViolation, want string) bool {
	for _, v := range violations {
		if v.Claim == want {
			return true
		}
	}
	return false
}

func slicesContains(items []string, target string) bool {
	for _, s := range items {
		if s == target {
			return true
		}
	}
	return false
}
