package toolvocabtest_test

import (
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
	coordFloorReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:      prompts.CoordinatorProfileID,
		SurfaceID:      "implement_investigate",
		SessionMode:    string(api.SessionModeAgent),
		Profiles:       profiles,
		Plan:           invPlan,
		Schemas:        schemas,
		RootCount:      1,
		HasGit:         true,
		HasAnyGitRoot:  true,
		WebSearch:      true,
		SpawnAllowlist: spawn.AmbientAllowedAgents(),
	})
	testutil.FailErr(t, "coord floor reader", err)

	askProfile := surface.ResolveTurnProfile(api.CoordinatorRunContext{}, &api.Session{Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild}, nil)
	askPlan, err := surface.CompileToolPlan(askProfile, 1)
	testutil.FailErr(t, "compile ask plan", err)
	coordAskReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:      prompts.CoordinatorProfileID,
		SurfaceID:      askProfile.SurfaceID,
		SessionMode:    string(api.SessionModeAsk),
		Profiles:       profiles,
		Plan:           askPlan,
		Schemas:        schemas,
		RootCount:      1,
		HasGit:         true,
		HasAnyGitRoot:  true,
		WebSearch:      true,
		SpawnAllowlist: prompts.ReadOnlyAgents(spawn.AmbientAllowedAgents()),
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

	t.Run("SubjectSelfRequestable admits loadable tool and rejects non-requestable edit", func(t *testing.T) {
		valid := toolvocabtest.EvaluateClaimBlock(baseCtx, toolvocabtest.ClaimBlock{
			Label:   "valid-loader",
			Subject: toolvocabtest.SubjectSelfRequestable(),
			Text:    "Load verification with `request_tools(need=\"verify\")`.",
		})
		if len(valid) > 0 {
			t.Fatalf("expected valid request_tools(need=\"verify\") to pass, got %+v", valid)
		}

		invalid := toolvocabtest.EvaluateClaimBlock(baseCtx, toolvocabtest.ClaimBlock{
			Label:   "invalid-edit-loader",
			Subject: toolvocabtest.SubjectSelfRequestable(),
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
				Subject: toolvocabtest.SubjectSelfRequestable(),
				Text:    "Call `request_tools(need=\"verify\")`.",
			})
			if len(unoffered) == 0 {
				t.Fatal("expected SubjectSelfRequestable to fail when request_tools is not offered")
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

		roWritePromiseFail := toolvocabtest.EvaluateClaimBlock(askCtx, toolvocabtest.ClaimBlock{
			Label:   "roster-path-explorer-write-promise",
			Subject: toolvocabtest.SubjectNamedAgent("path-explorer"),
			Text:    "Writes project files and applies changes with scope.mode write.",
		})
		if len(roWritePromiseFail) == 0 {
			t.Fatal("expected write promise on read-only path-explorer in Ask mode to fail")
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
		workerCloseout, rErr := engine.RenderKick(t.Context(), "worker-closeout", map[string]any{
			"reason_text": "step budget reached",
			"llm_timeout": true,
		})
		testutil.FailErr(t, "render worker-closeout", rErr)

		implAuth, err := toolvocabtest.ResolveWorkerAuthorityForSession(t.Context(), eff, api.SessionModeAgent, api.SessionPostureBuild, 1, true, true, false, "implementer")
		testutil.FailErr(t, "resolve implementer auth", err)
		roWorkerAuth, err := toolvocabtest.ResolveWorkerAuthorityForSession(t.Context(), eff, api.SessionModeAsk, api.SessionPostureBuild, 1, true, true, false, "path-explorer")
		testutil.FailErr(t, "resolve path-explorer auth", err)

		closeoutOnlyProfile := []sandbox.ToolProfile{{ID: "closeout_only", Tools: map[string]bool{"complete_leg": true}}}
		closeoutSelfReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "closeout_only", Profiles: closeoutOnlyProfile, Schemas: schemas, RootCount: 1,
		})
		testutil.FailErr(t, "closeout self reader", err)

		mutLegCtx := baseCtx
		mutLegCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(closeoutSelfReader, nil, nil)
		mutLegCtx.FollowupLeg = &implAuth
		if got := toolvocabtest.EvaluateClaimBlock(mutLegCtx, toolvocabtest.ClaimBlock{
			Label: "worker-closeout-implementer", Subject: toolvocabtest.SubjectFollowupLeg(), Text: workerCloseout,
		}); len(got) > 0 {
			t.Fatalf("expected worker-closeout follow-up advice to pass for mutating implementer leg, got %+v", got)
		}

		roLegCtx := mutLegCtx
		roLegCtx.SessionMode = api.SessionModeAsk
		roLegCtx.FollowupLeg = &roWorkerAuth
		if got := toolvocabtest.EvaluateClaimBlock(roLegCtx, toolvocabtest.ClaimBlock{
			Label: "worker-closeout-readonly", Subject: toolvocabtest.SubjectFollowupLeg(), Text: workerCloseout,
		}); len(got) == 0 {
			t.Fatal("expected mutating worker-closeout follow-up advice to fail for read-only worker leg")
		}

		coordCloseout, cErr := engine.RenderKick(t.Context(), "coordinator-turn-closeout", map[string]any{
			"reason_text": "turn limit reached",
			"llm_timeout": true,
		})
		testutil.FailErr(t, "render coordinator-turn-closeout", cErr)

		zeroToolProfile := []sandbox.ToolProfile{{ID: "zero_closeout", Tools: map[string]bool{}}}
		zeroSelfReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "zero_closeout", Profiles: zeroToolProfile, Schemas: schemas, RootCount: 1,
		})
		testutil.FailErr(t, "zero closeout reader", err)

		mutCoordAuth := toolvocabtest.AuthorityFromReaderAndMetas(coordFloorReader, nil, nil)
		askCoordAuth := toolvocabtest.AuthorityFromReaderAndMetas(coordAskReader, nil, nil)

		nextTurnAgentCtx := baseCtx
		nextTurnAgentCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(zeroSelfReader, nil, nil)
		nextTurnAgentCtx.SessionNextTurn = &mutCoordAuth
		if got := toolvocabtest.EvaluateClaimBlock(nextTurnAgentCtx, toolvocabtest.ClaimBlock{
			Label: "coord-closeout-agent", Subject: toolvocabtest.SubjectSessionNextTurn(), Text: coordCloseout,
		}); len(got) > 0 {
			t.Fatalf("expected coordinator-turn-closeout next-turn advice to pass in mutating Agent session, got %+v", got)
		}

		nextTurnAskCtx := nextTurnAgentCtx
		nextTurnAskCtx.SessionMode = api.SessionModeAsk
		nextTurnAskCtx.SessionNextTurn = &askCoordAuth
		if got := toolvocabtest.EvaluateClaimBlock(nextTurnAskCtx, toolvocabtest.ClaimBlock{
			Label: "coord-closeout-ask", Subject: toolvocabtest.SubjectSessionNextTurn(), Text: coordCloseout,
		}); len(got) == 0 {
			t.Fatal("expected mutating coordinator-turn-closeout next-turn advice to fail in Ask mode")
		}
	})

	t.Run("SubjectProhibition and SubjectHistoricalObservation", func(t *testing.T) {
		askCtx := baseCtx
		askCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(coordAskReader, nil, nil)

		if got := toolvocabtest.EvaluateClaimBlock(askCtx, toolvocabtest.ClaimBlock{
			Label:   "prohibition",
			Subject: toolvocabtest.SubjectProhibition(),
			Text:    "Do not call `promote_overlay` or `restore_version` on this turn.",
		}); len(got) > 0 {
			t.Fatalf("expected explicit prohibition to pass, got %+v", got)
		}

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

func TestClaimLevelNegationAndRequestToolsBinding(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog", err)
	stockSchemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "schemas", err)
	prunable, err := toolvocabtest.DiscoverPrunableSchemaIndex(t.Context(), eff)
	testutil.FailErr(t, "prunable", err)
	vocab, err := toolvocabtest.NewBacktickVocab(eff)
	testutil.FailErr(t, "vocab", err)

	mergedSchemas, err := toolschema.Merge(stockSchemas, toolschema.ConfigFromEntries(map[string]toolschema.Entry{
		"edit": {Description: "Edit file", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
	}))
	testutil.FailErr(t, "merge edit schema", err)
	customProfiles := []sandbox.ToolProfile{{
		ID:    "test_floor",
		Tools: map[string]bool{"command": true, "request_tools": true, "edit": true, "verify": true},
	}}
	catalog, err := toolvocab.NewCatalog(mergedSchemas, customProfiles, toolvocab.Provenance{})
	testutil.FailErr(t, "catalog with edit", err)

	planWithEditAndVerifyDeferred := toolsurface.Compile([]string{"command", "request_tools"}, []string{"edit", "verify"})
	readerWithEditReq, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "test_floor",
		Profiles:  customProfiles,
		Plan:      planWithEditAndVerifyDeferred,
		Schemas:   mergedSchemas,
		RootCount: 1,
		HasGit:    true,
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
		Catalog:     catalog,
		Prunable:    prunable,
		Vocab:       vocab,
		Self:        toolvocabtest.AuthorityFromReaderAndMetas(readerWithEditReq, nil, metas),
		SessionMode: api.SessionModeAgent,
		Posture:     api.SessionPostureBuild,
	}

	t.Run("affirmative without phrase fails and compound prohibition catches unnegated claim", func(t *testing.T) {
		gotWithoutDelay := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
			Label:   "affirmative-without-delay",
			Subject: toolvocabtest.SubjectSelfNow(),
			Text:    "Use `edit` without delay.",
		})
		if !hasViolationClaim(gotWithoutDelay, "edit") {
			t.Fatalf("expected 'Use `edit` without delay.' to fail naming edit, got %+v", gotWithoutDelay)
		}

		gotCompound := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
			Label:   "compound-prohibition-and-use",
			Subject: toolvocabtest.SubjectSelfNow(),
			Text:    "Do not call `delete_file`, and use `edit` now.",
		})
		if hasViolationClaim(gotCompound, "delete_file") || !hasViolationClaim(gotCompound, "edit") {
			t.Fatalf("expected delete_file excused and edit rejected, got %+v", gotCompound)
		}
	})

	t.Run("real prohibitions pass without excusing surrounding unoffered claims", func(t *testing.T) {
		for _, prose := range []string{
			"Do not call `edit` on this surface.",
			"`edit` is not available on this turn.",
			"Run `command` without `verification: true`.",
		} {
			if got := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
				Label:   "real-prohibition",
				Subject: toolvocabtest.SubjectSelfNow(),
				Text:    prose,
			}); len(got) > 0 {
				t.Fatalf("expected prohibition %q to pass, got %+v", prose, got)
			}
		}
	})

	t.Run("request_tools binds only to its requested target and rejects immediate use of another tool", func(t *testing.T) {
		mixed := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
			Label:   "mixed-loader-and-immediate-verify",
			Subject: toolvocabtest.SubjectSelfNow(),
			Text:    "Call `request_tools(need=\"edit\")` to load editing, and run `verify` now.",
		})
		if !hasViolationClaim(mixed, "verify") {
			t.Fatalf("expected unoffered verify to fail alongside request_tools(need=\"edit\"), got %+v", mixed)
		}

		validSpecific := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
			Label:   "valid-specific-loading-advice",
			Subject: toolvocabtest.SubjectSelfNow(),
			Text:    "Load `edit` via `request_tools(need=\"edit\")` before calling `edit`.",
		})
		if len(validSpecific) > 0 {
			t.Fatalf("expected valid specific loading advice for edit to pass, got %+v", validSpecific)
		}

		planWithoutEdit := toolsurface.Compile([]string{"command", "request_tools"}, []string{"verify"})
		readerWithoutEdit, rErr := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "test_floor", Profiles: customProfiles, Plan: planWithoutEdit,
			Schemas: mergedSchemas, RootCount: 1,
		})
		testutil.FailErr(t, "reader without edit", rErr)
		noEditCtx := evalCtx
		noEditCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(readerWithoutEdit, nil, metas)
		invalidTarget := toolvocabtest.EvaluateClaimBlock(noEditCtx, toolvocabtest.ClaimBlock{
			Label:   "unrequestable-edit-loader",
			Subject: toolvocabtest.SubjectSelfNow(),
			Text:    "Load `edit` via `request_tools(need=\"edit\")` before calling `edit`.",
		})
		if len(invalidTarget) == 0 {
			t.Fatal("expected loading unrequestable edit to fail")
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

	fullReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "postload_full", Profiles: fullProfiles, Plan: plan, Schemas: mergedSchemas, RootCount: 1, HasGit: true,
	})
	testutil.FailErr(t, "full reader", err)
	roReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "postload_ro", Profiles: roProfiles, Plan: plan, Schemas: mergedSchemas, RootCount: 1, HasGit: true,
	})
	testutil.FailErr(t, "ro reader", err)

	fullAuth := toolvocabtest.AuthorityFromReaderAndMetas(fullReader, nil, reqOnlyMetas)
	fullCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog: catalog, Prunable: prunable, Vocab: vocab, Self: fullAuth,
		SessionMode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
	}
	editAuth := fullAuth
	editAuth.PostLoadSchemas = map[string]map[string]any{"edit": editSchema}
	editCtx := fullCtx
	editCtx.Self = editAuth

	for _, okProse := range []string{
		`Load ` + "`edit`" + ` via ` + "`request_tools(need=\"edit\")`" + ` before setting ` + "`edit.file_path`" + `.`,
		`Load ` + "`edit`" + ` via ` + "`request_tools(need=\"edit\")`" + ` before calling ` + "`edit(file_path=\"a.go\", old_string=\"x\", new_string=\"y\")`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before passing ` + "`command.verification`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before calling ` + "`command(command=\"ls\", verification=true)`" + `.`,
	} {
		useCtx := fullCtx
		if slicesContains([]string{okProse[:15]}, "Load `edit` via") {
			useCtx = editCtx
		}
		if got := toolvocabtest.EvaluateClaimBlock(useCtx, toolvocabtest.ClaimBlock{Label: "postload-admitted", Subject: toolvocabtest.SubjectSelfNow(), Text: okProse}); len(got) > 0 {
			t.Fatalf("expected admitted post-load claim %q to pass, got %+v", okProse, got)
		}
	}

	nonexistent := toolvocabtest.EvaluateClaimBlock(editCtx, toolvocabtest.ClaimBlock{
		Label: "postload-nonexistent-field", Subject: toolvocabtest.SubjectSelfNow(),
		Text: `Load ` + "`edit`" + ` via ` + "`request_tools(need=\"edit\")`" + ` before setting ` + "`edit.nonexistent_field`" + `.`,
	})
	if !hasViolationClaim(nonexistent, "nonexistent_field") {
		t.Fatalf("expected nonexistent_field violation after edit load, got %+v", nonexistent)
	}

	roCtx := fullCtx
	roCtx.Posture = api.SessionPostureSpec
	roCtx.Self = toolvocabtest.AuthorityFromReaderAndMetas(roReader, nil, reqOnlyMetas)
	for _, prunedProse := range []string{
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before passing ` + "`command.verification`" + `.`,
		`Load ` + "`command`" + ` via ` + "`request_tools(need=\"command\")`" + ` before calling ` + "`command(command=\"ls\", verification=true)`" + `.`,
	} {
		got := toolvocabtest.EvaluateClaimBlock(roCtx, toolvocabtest.ClaimBlock{Label: "postload-pruned", Subject: toolvocabtest.SubjectSelfNow(), Text: prunedProse})
		if !hasViolationClaim(got, "verification") {
			t.Fatalf("expected pruned verification violation for %q, got %+v", prunedProse, got)
		}
	}

	loadOnlyAuth := fullAuth
	loadOnlyAuth.PostLoadSchemas = map[string]map[string]any{}
	loadOnlyCtx := fullCtx
	loadOnlyCtx.Self = loadOnlyAuth
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
	roReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "postload_cmd_ro", Profiles: roProfiles, Plan: plan, Schemas: stockSchemas, RootCount: 1, HasGit: true,
	})
	testutil.FailErr(t, "ro reader", err)
	reqMeta, _ := stockSchemas.ToolMeta("request_tools")
	roCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog:     catalog,
		Prunable:    prunable,
		Vocab:       vocab,
		Self:        toolvocabtest.AuthorityFromReaderAndMetas(roReader, nil, []tools.ToolMeta{{Name: "request_tools", ArgsSchema: reqMeta.ArgsSchema}}),
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
