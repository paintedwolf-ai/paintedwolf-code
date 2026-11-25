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
		r, rErr := prompts.NewReader(prompts.HostFacts{
			SessionMode: string(api.SessionModeAgent),
			Profiles:    profs,
			Schemas:     schemas,
			RootCount:   1,
			HasGit:      hasGit,
		}, prompts.TurnProjection{
			ProfileID:        prompts.CoordinatorProfileID,
			Plan:             readerLoadedPlan(plan, loaded),
			Offered:          readerLoadedOffer(plan, offered, loaded),
			ProjectedSchemas: projSchemas,
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
			name:    "a_mention_claims_reach_whatever_its_polarity",
			ctxVal:  roGitCtx,
			ctxInv:  roGitCtx,
			valid:   "Do not call `recall` here.",
			invalid: "Do not call `verify` here.",
		},
		{
			name:    "a_bare_field_span_claims_nothing_but_a_call_argument_does",
			ctxVal:  buildCtx,
			ctxInv:  buildCtx,
			valid:   "Pass `questions` in `command` to ask the user.",
			invalid: "Call `recall(nonexistent_field=1)` when querying.",
		},
		{
			name:    "call_argument_enum_admitted_vs_narrowed",
			ctxVal:  buildCtx,
			ctxInv:  buildCtx,
			valid:   "Call `wait(conditions=[{\"kind\":\"http_ready\"}], …)` for readiness.",
			invalid: "Call `wait(conditions=[{\"kind\":\"process_done\"}], …)` for readiness.",
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
			name:    "loader_before_call_requestable_vs_unrequestable",
			ctxVal:  buildCtx,
			ctxInv:  roGitCtx,
			valid:   "Call `request_tools(need=\"verify\")`, then `verify(…)`.",
			invalid: "Call `request_tools(need=\"verify\")`, then `verify(…)`.",
		},
		{
			name:    "loader_must_precede_the_call_in_its_block",
			ctxVal:  buildCtx,
			ctxInv:  buildCtx,
			valid:   "- `request_tools(need=\"verify\")` then `verify(…)`",
			invalid: "- `verify(…)` after `request_tools(need=\"verify\")`",
		},
		{
			name:    "loader_in_another_block_does_not_hold_the_tool",
			ctxVal:  buildCtx,
			ctxInv:  buildCtx,
			valid:   "Mention `verify` freely.\n\nCall `request_tools(need=\"verify\")` and `verify(…)`.",
			invalid: "Call `request_tools(need=\"verify\")`.\n\nCall `verify(…)`.",
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

	t.Run("skill_admission_reads_mentions_and_loader_calls", func(t *testing.T) {
		skSequenced := skills.Parse("seq-probe", []byte("---\nname: seq-probe\ndescription: Sequenced loader probe.\n---\nCall `request_tools(need=\"command\")`, then `command(command=\"git status\")`.\n"))
		skUnsequenced := skills.Parse("unseq-probe", []byte("---\nname: unseq-probe\ndescription: Unsequenced probe.\n---\nRun `command(command=\"git status\")` directly.\n"))
		skMention := skills.Parse("mention-probe", []byte("---\nname: mention-probe\ndescription: Mention probe.\n---\nInspection runs in `command`.\n"))

		defPlan := toolsurface.Compile([]string{"request_tools", "recall"}, []string{"command"})
		defReader, err := prompts.NewReader(prompts.HostFacts{
			Profiles:  profiles,
			RootCount: 1,
			HasGit:    true,
		}, prompts.TurnProjection{
			ProfileID: prompts.CoordinatorProfileID,
			Plan:      readerLoadedPlan(defPlan, map[string]bool{"request_tools": true, "recall": true}),
			Offered:   readerLoadedOffer(defPlan, []string{"request_tools", "recall", "command"}, map[string]bool{"request_tools": true, "recall": true}),
		})
		if err != nil {
			t.Fatalf("defReader: %v", err)
		}
		noDefPlan := toolsurface.Compile([]string{"request_tools", "recall"}, nil)
		noDefReader, err := prompts.NewReader(prompts.HostFacts{
			Profiles:  profiles,
			RootCount: 1,
			HasGit:    true,
		}, prompts.TurnProjection{
			ProfileID: prompts.CoordinatorProfileID,
			Plan:      readerLoadedPlan(noDefPlan, map[string]bool{"request_tools": true, "recall": true}),
			Offered:   readerLoadedOffer(noDefPlan, []string{"request_tools", "recall"}, map[string]bool{"request_tools": true, "recall": true}),
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
		if !toolvocab.SkillToolsOfferedForReader(skMention.Skill, defReader) {
			t.Fatal("a mention of a requestable tool was rejected")
		}
	})
}
