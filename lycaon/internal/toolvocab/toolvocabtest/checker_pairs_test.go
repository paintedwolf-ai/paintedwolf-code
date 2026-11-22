package toolvocabtest_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
)

type checkerPairFixture struct {
	profiles   []sandbox.ToolProfile
	vocab      *toolvocabtest.BacktickVocab
	buildCtx   toolvocabtest.ClaimEvaluationContext
	roGitCtx   toolvocabtest.ClaimEvaluationContext
	roNoGitCtx toolvocabtest.ClaimEvaluationContext
}

func buildCheckerPairFixture(t *testing.T) checkerPairFixture {
	t.Helper()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "discover stock", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "schemas", err)
	cat, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	idx, err := toolvocabtest.StockPrunableSchemaIndex()
	testutil.FailErr(t, "prunable index", err)
	vocab, err := toolvocabtest.StockBacktickVocab()
	testutil.FailErr(t, "backtick vocab", err)

	cmdMeta, _ := schemas.ToolMeta("command")
	waitMeta, _ := schemas.ToolMeta("wait")
	recallMeta, _ := schemas.ToolMeta("recall")
	reqMeta, _ := schemas.ToolMeta("request_tools")
	verifyMeta, _ := schemas.ToolMeta("verify")

	roEffects, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	roProfiles := make([]sandbox.ToolProfile, len(profiles))
	copy(roProfiles, profiles)
	for i := range roProfiles {
		roProfiles[i].CommandEffects &= roEffects
	}
	roCmdSchema := toolschema.ProjectToolArgsSchema("command", cmdMeta.ArgsSchema, toolschema.ProjectOptions{CommandReadOnly: true})
	narrowWaitSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"until_complete": map[string]any{"type": "boolean"},
			"conditions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": []any{"http_ready", "port_ready"}},
					},
				},
			},
		},
	}

	gitProgTable := toolreach.NewProgramTable(
		map[string]bool{"outline": true, "lsof": true, "echo": true, "sed": true, "git": true, "git status": true, "git show": true},
		map[string]map[string]bool{
			"outline":    {"default": true, "read": true},
			"lsof":       {"default": true, "read": true, "port_probe": true},
			"echo":       {"default": true, "read": true},
			"sed":        {"stream-script": true},
			"git":        {"default": true, "read": true, "status": true},
			"git status": {"default": true, "query": true},
			"git show":   {"default": true, "query": true},
		},
	)
	noGitProgTable := toolreach.NewProgramTable(
		map[string]bool{"outline": true, "sed": true},
		map[string]map[string]bool{
			"outline": {"default": true, "read": true},
			"sed":     {"stream-script": true},
		},
	)

	makeCtx := func(
		profs []sandbox.ToolProfile,
		offered, deferred []string,
		projSchemas, postSchemas map[string]map[string]any,
		progTable *toolreach.ProgramTable,
		hasGit bool,
	) toolvocabtest.ClaimEvaluationContext {
		plan := toolsurface.Compile(offered, deferred)
		loaded := map[string]bool{}
		for _, o := range offered {
			loaded[o] = true
		}
		r, rErr := prompts.NewReader(prompts.ReaderInput{
			ProfileID:        prompts.CoordinatorProfileID,
			SessionMode:      string(api.SessionModeAgent),
			Profiles:         profs,
			Plan:             plan,
			Loaded:           loaded,
			Visible:          offered,
			Schemas:          schemas,
			ProjectedSchemas: projSchemas,
			RootCount:        1,
			HasGit:           hasGit,
		})
		testutil.FailErr(t, "NewReader", rErr)
		return toolvocabtest.ClaimEvaluationContext{
			Catalog:  cat,
			Prunable: idx,
			Vocab:    vocab,
			Self: toolvocabtest.ActorAuthority{
				Reader:           r,
				ProgramTable:     progTable,
				PostLoadPrograms: progTable,
				Schemas:          projSchemas,
				PostLoadSchemas:  postSchemas,
			},
			SessionMode: api.SessionModeAgent,
			Posture:     api.SessionPostureBuild,
		}
	}

	buildOffered := []string{"command", "recall", "request_tools", "wait"}
	buildSchemas := map[string]map[string]any{
		"command":       cmdMeta.ArgsSchema,
		"recall":        recallMeta.ArgsSchema,
		"request_tools": reqMeta.ArgsSchema,
		"wait":          narrowWaitSchema,
	}
	roSchemas := map[string]map[string]any{
		"command":       roCmdSchema,
		"recall":        recallMeta.ArgsSchema,
		"request_tools": reqMeta.ArgsSchema,
		"wait":          waitMeta.ArgsSchema,
	}

	return checkerPairFixture{
		profiles:   profiles,
		vocab:      vocab,
		buildCtx:   makeCtx(profiles, buildOffered, []string{"verify"}, buildSchemas, map[string]map[string]any{"verify": verifyMeta.ArgsSchema}, gitProgTable, true),
		roGitCtx:   makeCtx(roProfiles, buildOffered, nil, roSchemas, nil, gitProgTable, true),
		roNoGitCtx: makeCtx(roProfiles, buildOffered, nil, roSchemas, nil, noGitProgTable, false),
	}
}

func TestCheckerRefinementsValidAndNearbyInvalidPairs(t *testing.T) {
	fx := buildCheckerPairFixture(t)
	buildCtx, roGitCtx, roNoGitCtx := fx.buildCtx, fx.roGitCtx, fx.roNoGitCtx
	vocab, profiles := fx.vocab, fx.profiles

	eval := func(ctx toolvocabtest.ClaimEvaluationContext, text string) []toolvocabtest.SubjectViolation {
		return toolvocabtest.EvaluateClaimBlock(ctx, toolvocabtest.ClaimBlock{
			Label: "checker-pair", Subject: toolvocabtest.SubjectSelfNow(), Text: text,
		})
	}

	cases := []struct {
		name    string
		ctxVal  toolvocabtest.ClaimEvaluationContext
		ctxInv  toolvocabtest.ClaimEvaluationContext
		valid   string
		invalid string
	}{
		{
			name:    "in_command_program_context_vs_foreign_schema_field",
			ctxVal:  buildCtx,
			ctxInv:  buildCtx,
			valid:   "Run `outline` in `command` to inspect file symbols.",
			invalid: "Pass `questions` in `command` to ask the user.",
		},
		{
			name:    "host_resource_cli_kind_in_command_vs_key_value_schema_claim",
			ctxVal:  buildCtx,
			ctxInv:  buildCtx,
			valid:   "Run `kubectl`, `helm`, or `kind` in `command` and declare the id in `capability_request.host_resources` on that call.",
			invalid: "Set `kind: \"process_done\"` in `command` when waiting.",
		},
		{
			name:    "single_enum_path_for_literal_admitted_vs_narrowed",
			ctxVal:  buildCtx,
			ctxInv:  buildCtx,
			valid:   "Use `wait` with `http_ready` for readiness.",
			invalid: "Use `wait` with `process_done` for readiness.",
		},
		{
			name:    "nested_property_suffix_admitted_vs_pruned_on_readonly",
			ctxVal:  buildCtx,
			ctxInv:  roGitCtx,
			valid:   "Request `local_listen` on `command` when starting a dev server.",
			invalid: "Request `local_listen` on `command` when starting a dev server.",
		},
		{
			name:    "unknown_preposition_target_vs_known_tool_missing_field",
			ctxVal:  buildCtx,
			ctxInv:  buildCtx,
			valid:   "Keep `state` on `session` when resuming.",
			invalid: "Pass `nonexistent_field` on `recall` when querying.",
		},
		{
			name:    "negation_no_span_and_will_not_stand_in_vs_affirmative_unoffered",
			ctxVal:  roGitCtx,
			ctxInv:  roGitCtx,
			valid:   "This surface has no `verify`, `survey_repo` will not stand in for exact source reads, and do not open with `fetch_url`.",
			invalid: "This surface offers `verify`, `survey_repo` stands in for source reads, and open with `fetch_url`.",
		},
		{
			name:    "angle_placeholder_vs_unquoted_output_redirect_on_readonly",
			ctxVal:  roGitCtx,
			ctxInv:  roGitCtx,
			valid:   "Inspect open descriptors with `lsof -iTCP:8080` or `git show <rev>` in `command`.",
			invalid: "Write `echo ready > status.txt` in `command`.",
		},
		{
			name:    "sed_print_admitted_vs_sed_inplace_rejected_on_readonly",
			ctxVal:  roGitCtx,
			ctxInv:  roGitCtx,
			valid:   "Slice bounded lines with `sed -n` in `command`.",
			invalid: "Rewrite lines in place with `sed -i 's/a/b/' a.go` in `command`.",
		},
		{
			name:    "git_config_override_admitted_with_git_vs_rejected_without_git",
			ctxVal:  roGitCtx,
			ctxInv:  roNoGitCtx,
			valid:   "Scope the CA bundle with `git -c http.sslCAInfo=ca.pem` in `command`.",
			invalid: "Scope the CA bundle with `git -c http.sslCAInfo=ca.pem` in `command`.",
		},
		{
			name:    "after_loading_it_with_loader_requestable_vs_unrequestable",
			ctxVal:  buildCtx,
			ctxInv:  roGitCtx,
			valid:   "Run `verify(…)` after loading it with `request_tools(need=\"verify\")`.",
			invalid: "Run `verify(…)` after loading it with `request_tools(need=\"verify\")`.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eval(tc.ctxVal, tc.valid); len(got) != 0 {
				t.Fatalf("valid example unexpectedly failed: %+v\ntext: %s", got, tc.valid)
			}
			if got := eval(tc.ctxInv, tc.invalid); len(got) == 0 {
				t.Fatalf("nearby invalid example unexpectedly passed:\ntext: %s", tc.invalid)
			}
		})
	}

	t.Run("scoped_catalog_id_and_secret_ref_vs_unclassified_backtick", func(t *testing.T) {
		validProse := "Check `hitl_consulted:intake` and pass `{{paintedwolf-secret:db_url}}` unchanged."
		if diags := vocab.CheckProse("unit/test", validProse); len(diags) != 0 {
			t.Fatalf("valid catalog/secret backticks flagged: %+v", diags)
		}
		invalidProse := "Check `unknown_prefix:intake` and pass `unclassifiedCamelIdent` unchanged."
		if diags := vocab.CheckProse("unit/test", invalidProse); len(diags) < 2 {
			t.Fatalf("expected nearby invalid backticks to fail CheckProse, got %+v", diags)
		}
	})

	t.Run("skill_admission_loader_sequenced_requestable_vs_unrequestable_or_unsequenced", func(t *testing.T) {
		skSequenced := skills.Parse("seq-probe", []byte("---\nname: seq-probe\ndescription: Sequenced loader probe.\n---\nLoad `command` via `request_tools(need: \"stage and commit git changes\")`.\n"))
		skUnsequenced := skills.Parse("unseq-probe", []byte("---\nname: unseq-probe\ndescription: Unsequenced probe.\n---\nRun `command` directly without loading.\n"))

		defPlan := toolsurface.Compile([]string{"request_tools", "recall"}, []string{"command"})
		defReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: prompts.CoordinatorProfileID, Profiles: profiles,
			Plan: defPlan, Loaded: map[string]bool{"request_tools": true, "recall": true},
			Visible: []string{"request_tools", "recall", "command"}, RootCount: 1, HasGit: true,
		})
		if err != nil {
			t.Fatalf("defReader: %v", err)
		}
		noDefPlan := toolsurface.Compile([]string{"request_tools", "recall"}, nil)
		noDefReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: prompts.CoordinatorProfileID, Profiles: profiles,
			Plan: noDefPlan, Loaded: map[string]bool{"request_tools": true, "recall": true},
			Visible: []string{"request_tools", "recall"}, RootCount: 1, HasGit: true,
		})
		if err != nil {
			t.Fatalf("noDefReader: %v", err)
		}
		if !toolvocab.SkillToolsOfferedForReader(skSequenced.Skill, defReader) {
			t.Fatal("valid loader-sequenced requestable skill was unexpectedly rejected")
		}
		if toolvocab.SkillToolsOfferedForReader(skSequenced.Skill, noDefReader) {
			t.Fatal("nearby invalid loader-sequenced skill passed when target was not requestable")
		}
		if toolvocab.SkillToolsOfferedForReader(skUnsequenced.Skill, defReader) {
			t.Fatal("nearby invalid unsequenced deferred-tool skill passed without loader")
		}
	})
}
