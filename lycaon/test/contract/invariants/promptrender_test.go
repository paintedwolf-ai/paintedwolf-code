package invariants_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

const guidance = "lycaon/config/packs/painted-wolf/platform/guidance/"

var templateTree = map[string]string{
	guidance + "coordinator-roots-changed.md":                                      "Roots changed: {{ root.label }}.\n",
	guidance + "editor/inline-edit.md":                                             "Editing {{ path }}.\n",
	guidance + "curator/user.md":                                                   "Evidence at {{ c.path }}.\n",
	"lycaon/config/packs/painted-wolf/platform/shared/partials/command-surface.md": "{% if session_scratch_dir %}scratch{% endif %}\n",
}

// The family fails on each kind of drift, shown on the template host-path
// row; the rendering rows read the stock catalog.
func TestPromptRenderFailsClosed(t *testing.T) {
	t.Parallel()
	hostPaths := rule(t, promptrender.Family, "templates-render-no-host-paths")
	stale := bare(hostPaths)
	stale.Exempt = []invariants.Exemption{{Key: "config/packs/painted-wolf/platform/guidance/retired.md: r.path", Reason: "fixture"}}
	for name, tc := range map[string]struct {
		rule       invariants.Invariant
		files      map[string]string
		want       outcome
		violations int
	}{
		"clean": {rule: hostPaths, files: templateTree, want: passes},
		// Every host path variable is caught, and a coordinator kick may not
		// render a bare path.
		"seeded violation": {rule: hostPaths, files: with(templateTree, map[string]string{
			guidance + "coordinator-roots-changed.md": "{{ r.path }} {{ c.old_path }} {{ new_primary.path }} {{ active_root.path }} {{ root.path }} {{ project_dir }} {{ session_scratch_dir }} {{ worker_workspace_path }} {{ path }}\n",
		}), want: violates, violations: 9},
		"renamed anchor": {rule: hostPaths, files: map[string]string{
			"lycaon/config/packs/painted-wolf/platform/notes/x.md": "{{ path }}\n",
		}, want: missesAnchor},
		"stale exemption": {rule: stale, files: templateTree, want: staleExemption},
		"empty corpus": {rule: hostPaths, files: map[string]string{
			guidance + "notes.txt": "not a template\n",
		}, want: fails},
		"unparsable file": {rule: rule(t, promptrender.Family, "skills-fit-their-budgets"), files: map[string]string{
			"lycaon/internal/skills/budget.go": "package skills\n\nconst BundledBodyBudget = (\n",
		}, want: fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, invariants.Evaluate(fixture(t, tc.files), tc.rule), tc.want, tc.violations)
		})
	}
}

const seededKick = "coordinator-roots-changed"

// P13 on the coordinator kick row: a guidance template, served through the
// checkout's prompt overlay, that names a tool no coordinator surface offers
// fails on every surface; the stock template and a rewording of it that keeps
// its names pass.
func TestPromptRenderReachFailsOnASeededTemplate(t *testing.T) {
	t.Parallel()
	kicks := rule(t, promptrender.Family, "coordinator-kicks-name-only-offered-tools")
	stockKick, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), guidance+seededKick+".md"))
	contractcheck.FailErr(t, "read the stock kick", err)
	overlay := "lycaon/" + protectedpath.PromptFilesDirName + "/kicks/" + seededKick + ".md"
	anchors := map[string]string{
		"lycaon/internal/prompts/render.go": "package prompts\n\ntype FileTemplateEngine struct{}\n\nfunc (e *FileTemplateEngine) RenderKick() {}\n",
	}
	reworded := "To keep you oriented: " + strings.ReplaceAll(string(stockKick), "\n\n", "\n\nIn addition: ")
	seeded := string(stockKick) + "\nTo settle this, call `complete_leg`.\n"
	// No tool schema has wait_ms, so the argument is stale wherever it is
	// attributed.
	staleArg := string(stockKick) + "\nAsk again with `ask_user(prompt=…, wait_ms=500)`.\n"
	ownArg := string(stockKick) + "{% if profile_has_ask_user %}\nAsk again with `ask_user(prompt=…)`.\n{% endif %}"
	for name, tc := range map[string]struct {
		overlay string
		want    outcome
		names   []string
	}{
		"stock template":  {want: passes},
		"reworded":        {overlay: reworded, want: passes},
		"seeded tool":     {overlay: seeded, want: violates, names: []string{"complete_leg"}},
		"seeded argument": {overlay: staleArg, want: violates, names: []string{"wait_ms", "ask_user"}},
		"own argument":    {overlay: ownArg, want: passes},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := with(anchors, map[string]string{overlay: tc.overlay})
			res := invariants.Evaluate(fixture(t, files), kicks)
			expect(t, res, tc.want, 0)
			for _, v := range res.Violations {
				if !strings.Contains(v, "kick "+seededKick+" ") || !slices.ContainsFunc(tc.names, func(n string) bool { return strings.Contains(v, n) }) {
					t.Errorf("violation outside the seeded template and %v: %s", tc.names, v)
				}
			}
			if len(tc.names) > 0 && !slices.ContainsFunc(res.Violations, func(v string) bool { return strings.Contains(v, tc.names[0]) }) {
				t.Errorf("no violation reports %s", tc.names[0])
			}
		})
	}
}

func TestCapabilityConditionsAndSourceDiscoveryFailClosed(t *testing.T) {
	t.Parallel()
	capRule := rule(t, promptrender.Family, "capability-conditions-use-declared-facts")
	discRule := rule(t, promptrender.Family, "stock-sources-reconciled-and-parsed")

	anchors := map[string]string{
		"lycaon/internal/prompts/reader_facts_vocab.go": "package prompts\n\nfunc CheckTemplateVocabulary() {}\n",
		"lycaon/internal/prompts/persona_render.go":     "package prompts\n\nfunc RenderPersona() {}\n",
	}

	const (
		skillDir     = "lycaon/config/packs/painted-wolf/platform/skills/seeded-release/"
		skillBodyRel = skillDir + "SKILL.md"
		skillRefRel  = skillDir + "references/seeded-manifest.md"
		promptRel    = "lycaon/config/packs/painted-wolf/platform/agents/prompts/seeded-agent.md"
		partialRel   = "lycaon/config/packs/painted-wolf/platform/shared/partials/seeded-host.md"
	)

	t.Run("passing controls for valid capability guards and presentation logic", func(t *testing.T) {
		t.Parallel()
		files := with(anchors, map[string]string{
			skillBodyRel: "---\nname: seeded-release\ndescription: Valid control skill.\nmetadata:\n  paintedwolf.any_of_tools: command, verify\n---\n" +
				"{% if multi_root %}Workspace spans {{ root_count }} folders.{% endif %}\n" +
				"{% if has_git and not on_worker_branch %}Resolve commit, tag, and version.{% else %}Take version from the brief.{% endif %}\n" +
				"{% if can_git_read %}Inspect `git log` in `command`.{% elif can_request_git_read %}Call `request_tools(need=\"command\")`.{% endif %}\n" +
				"{% if profile_has_verify %}Run `verify`.{% endif %}\n" +
				"{% if can_command_external_selector %}Run `external -- lsof +L1`.{% endif %}\n",
			partialRel: "{% if runner_host_resources_tools %}Declare id in `capability_request.host_resources`.{% endif %}\n",
			promptRel:  "{% set finish_note = \"Report grounded results.\" %}{{ finish_note }}\n{% include \"partials/seeded-host.md\" %}\n",
		})
		expect(t, invariants.Evaluate(fixture(t, files), discRule), passes, 0)
		expect(t, invariants.Evaluate(fixture(t, files), capRule), passes, 0)
	})

	for name, tc := range map[string]struct {
		rule    invariants.Invariant
		files   map[string]string
		expects []string
	}{
		"seeded local set alias over real vocabulary promising unavailable git action": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Seeded alias.\n---\n" +
					"{% set git_ok = has_git and not on_worker_branch %}\n" +
					"{% if git_ok %}Run `git log previous..release` in `command`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/SKILL.md",
				"has_git and not on_worker_branch",
				"git_ok",
			},
		},
		"seeded inline workspace condition promising unestablished command and verify": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Seeded inline.\n---\n" +
					"{% if has_git and not on_worker_branch %}Confirm `git status --short` in `command` and run `verify`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/SKILL.md",
				"has_git and not on_worker_branch",
				"verify",
			},
		},
		"seeded violation inside included partial": {
			rule: capRule,
			files: map[string]string{
				partialRel: "{% if (profile_has_command and profile_command_external_exec != false) or profile_has_verify %}Include id in `capability_request.host_resources`.{% endif %}\n",
				promptRel:  "## Host\n{% include \"partials/seeded-host.md\" %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/shared/partials/seeded-host.md",
				"profile_has_verify",
				"capability_request.host_resources",
			},
		},
		"seeded violation inside skill reference file": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Valid body.\nmetadata:\n  paintedwolf.any_of_tools: command, verify\n---\n{% if profile_has_verify %}Run `verify`.{% endif %}\n",
				skillRefRel:  "{% if not on_worker_branch %}Compute `git log` in `command` and `external -- lsof +L1`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/references/seeded-manifest.md",
				"not on_worker_branch",
				"can_command_external_selector",
			},
		},
		"seeded static tool_profile check reconstructing capability": {
			rule: capRule,
			files: map[string]string{
				partialRel: "{% if tool_profile == \"implement\" %}Request access in `capability_request`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/shared/partials/seeded-host.md",
				`tool_profile == \"implement\"`,
				"checks static tool_profile identity",
			},
		},
		"seeded skill mentioning unavailable argument in own text fails even when owning tool command is offered": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Self-justifying argument claim.\n---\n" +
					"Pass `socks_proxy: true` and `terminal_capture` on `command`.\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/SKILL.md",
				"<unguarded>",
				"socks_proxy: true",
				"terminal_capture",
			},
		},
		"seeded skill mentioning curl in command without egress fact fails": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Self-justifying curl claim.\n---\n" +
					"Run `curl -sS https://example.com` in `command`.\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/SKILL.md",
				"<unguarded>",
				"promises action \"curl\" without profile_command_network_egress",
			},
		},
		"seeded profile_command_mutation guard promising git commit fails without can_git_commit": {
			rule: capRule,
			files: map[string]string{
				partialRel: "{% if profile_command_mutation %}Stage and run `git commit -m msg -- path` in `command`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/shared/partials/seeded-host.md",
				"profile_command_mutation",
				"can_git_commit",
			},
		},
		"seeded profile_has_ask_user guard promising response_type secret fails without can_ask_user_secret": {
			rule: capRule,
			files: map[string]string{
				partialRel: "{% if profile_has_ask_user %}Request credentials via `ask_user` (`response_type: secret`).{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/shared/partials/seeded-host.md",
				"profile_has_ask_user",
				"response_type: secret",
			},
		},
		"seeded skill naming ask_user unguardedly promising response_type secret fails under narrower projected schema": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Mentions ask_user and secret response.\n---\n" +
					"Obtain credentials via `ask_user` with `response_type: secret`.\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/SKILL.md",
				"<unguarded>",
				"response_type: secret",
			},
		},
		"newly added unclassified stock markdown source fails discovery": {
			rule: discRule,
			files: map[string]string{
				"lycaon/config/packs/painted-wolf/platform/unreviewed/rogue-prompt.md": "Call `verify`.\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/unreviewed/rogue-prompt.md",
				"unclassified",
			},
		},
		"unhandled executable Pongo tag fails closed naming source": {
			rule: discRule,
			files: map[string]string{
				partialRel: "{% custom_exec profile_has_command %}run{% endcustom_exec %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/shared/partials/seeded-host.md",
				"custom_exec",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(fixture(t, with(anchors, tc.files)), tc.rule)
			expect(t, res, violates, 0)
			joined := strings.Join(res.Violations, "\n")
			for _, want := range tc.expects {
				if !strings.Contains(joined, want) {
					t.Errorf("violations missing %q:\n%s", want, joined)
				}
			}
		})
	}
}

func TestProductionSkillAdmissionGuaranteesAndNonGuarantees(t *testing.T) {
	t.Parallel()
	s, err := promptrender.LoadStock(t.Context())
	contractcheck.FailErr(t, "LoadStock", err)

	cmdArgSkill := skills.Parse("seeded-cmd-arg", []byte(
		"---\nname: seeded-cmd-arg\ndescription: Mentions command and pruned args.\n---\n"+
			"Run `curl -sS https://example.com` and `git commit -m x` with `socks_proxy: true` and `terminal_capture` in `command`.\n",
	)).Skill

	if toolvocab.SkillToolsOffered(cmdArgSkill, map[string]bool{"recall": true}) {
		t.Fatal("SkillToolsOffered admitted skill naming `command` when `command` is absent")
	}
	readOnlyCommandHeld := map[string]bool{"command": true}
	if !toolvocab.SkillToolsOffered(cmdArgSkill, readOnlyCommandHeld) {
		t.Fatal("expected SkillToolsOffered to admit skill on read-only command (proving admission does NOT check curl, git commit, socks_proxy, or terminal_capture)")
	}
	if toolcontract.CommandLanguageReachable("curl", readOnlyCommandHeld) || toolcontract.CommandLanguageReachable("git commit", readOnlyCommandHeld) {
		t.Fatal("read-only command held set unexpectedly reached curl or git commit")
	}
	cmdMeta, ok := s.Schemas.ToolMeta("command")
	if !ok {
		t.Fatal("missing command schema")
	}
	roCmdSchema := toolschema.PruneCommandReadOnlySchema(cmdMeta.ArgsSchema)
	for _, prunedField := range []string{"capability_request", "capability_request.host_resources", "secret_use", "socks_proxy", "snapshot_capture", "terminal_capture", "verification"} {
		if toolschema.SchemaAcceptsField(roCmdSchema, prunedField) {
			t.Fatalf("read-only projected command schema unexpectedly accepted %q", prunedField)
		}
	}

	verifySkill := skills.Parse("seeded-verify-runner", []byte(
		"---\nname: seeded-verify-runner\ndescription: Mentions verify.\n---\nCall `verify`.\n",
	)).Skill
	if toolvocab.SkillToolsOffered(verifySkill, readOnlyCommandHeld) {
		t.Fatal("SkillToolsOffered admitted skill naming `verify` without `verify` in held")
	}
	if !toolvocab.SkillToolsOffered(verifySkill, map[string]bool{"verify": true}) {
		t.Fatal("SkillToolsOffered rejected skill naming `verify` when `verify` is held")
	}
	verifyMeta, ok := s.Schemas.ToolMeta("verify")
	if !ok {
		t.Fatal("missing verify schema")
	}
	projVerify := toolschema.ProjectToolArgsSchema("verify", verifyMeta.ArgsSchema, toolschema.ProjectOptions{})
	for _, keptField := range []string{"capability_request", "capability_request.host_resources", "secret_use", "socks_proxy"} {
		if !toolschema.SchemaAcceptsField(projVerify, keptField) {
			t.Fatalf("projected verify schema missing %q", keptField)
		}
	}

	askFields, err := promptrender.SkillGuaranteedSchemaFieldsForText(s,
		"---\nname: ask-probe\ndescription: Probe.\n---\nCall `ask_user`.\n",
	)
	contractcheck.FailErr(t, "SkillGuaranteedSchemaFieldsForText ask_user", err)
	if !slices.Contains(askFields, "prompt") || slices.Contains(askFields, "secret") || slices.Contains(askFields, "secret.name") {
		t.Fatalf("ask_user retained skill schema guarantees = %v, must keep prompt and exclude Ask-pruned secret*", askFields)
	}
	waitFields, err := promptrender.SkillGuaranteedSchemaFieldsForText(s,
		"---\nname: wait-probe\ndescription: Probe.\n---\nCall `wait`.\n",
	)
	contractcheck.FailErr(t, "SkillGuaranteedSchemaFieldsForText wait", err)
	if !slices.Contains(waitFields, "timeout_ms") || slices.Contains(waitFields, "conditions") || slices.Contains(waitFields, "conditions.kind") {
		t.Fatalf("wait retained skill schema guarantees = %v, must keep timeout_ms and exclude timer-only-pruned conditions*", waitFields)
	}

	overlays, err := hoststate.DiscoverPackOverlays(t.Context())
	contractcheck.FailErr(t, "DiscoverPackOverlays", err)
	stockOverlay := overlays[0]
	stockSkills, err := promptrender.StockSkills(s)
	contractcheck.FailErr(t, "StockSkills", err)

	skillRetainedFields := make(map[string][]string, len(stockSkills))
	for _, sk := range stockSkills {
		rawBytes, rErr := os.ReadFile(filepath.Join("..", "..", "..", "config", "packs", sk.PackID, "skills", sk.Name, "SKILL.md"))
		contractcheck.FailErr(t, "read stock skill "+sk.Name, rErr)
		fields, fErr := promptrender.SkillGuaranteedSchemaFieldsForText(s, string(rawBytes))
		contractcheck.FailErr(t, "SkillGuaranteedSchemaFieldsForText "+sk.Name, fErr)
		skillRetainedFields[sk.Name] = fields
	}

	checkAuthoritySkills := func(label string, held map[string]bool, offered []string, reqSchemas map[string]map[string]any) {
		t.Helper()
		for _, sk := range stockSkills {
			if !toolvocab.SkillToolsOffered(sk, held) {
				continue
			}
			for _, field := range skillRetainedFields[sk.Name] {
				accepted := false
				for _, tName := range offered {
					if toolschema.SchemaAcceptsField(reqSchemas[tName], field) {
						accepted = true
						break
					}
				}
				if !accepted {
					t.Fatalf("%s admitted stock skill %q, but retained schema guarantee %q is absent from narrowed offered schemas (offered=%v)",
						label, sk.Name, field, offered)
				}
			}
		}
	}

	for _, posture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureBuild} {
		for _, mode := range []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk} {
			runCtx := api.CoordinatorRunContext{}
			surfID := "implement_routing"
			if mode == api.SessionModeAsk {
				surfID = "observe_investigate"
				runCtx = api.CoordinatorRunContext{
					RunID: "ask-wf", WorkflowID: "wf", PhaseCoordinatorSurface: "implement_routing",
				}
			}
			tp := surface.ResolveTurnProfileForSurface(surfID, runCtx, &api.Session{Mode: mode, Posture: posture})
			pl, pErr := surface.CompileToolPlan(tp, 1)
			contractcheck.FailErr(t, "CompileToolPlan", pErr)
			loaded := map[string]bool{}
			for _, d := range pl.DeferredNames() {
				loaded[d] = true
			}
			auth, fErr := hoststate.FreezeTurnAuthority(t.Context(), hoststate.CoordinatorState{
				Overlay: stockOverlay, SessionMode: mode, Posture: posture, RootCount: 1,
				HasGit: true, WebSearch: true, RunContext: runCtx, TurnProfile: tp,
				Plan: pl, Loaded: loaded, SpawnAllowlist: stockOverlay.SpawnAllowlist,
			})
			contractcheck.FailErr(t, "FreezeTurnAuthority", fErr)
			checkAuthoritySkills("coord/"+string(posture)+"/"+string(mode), auth.Reader.Held(), auth.Offered, auth.Schemas)

			wAuth, wErr := hoststate.FreezeWorkerAuthority(t.Context(), hoststate.WorkerState{
				Overlay: stockOverlay, AgentID: "implementer", ToolProfile: "implement",
				Posture: posture, ParentSessionMode: mode, RootCount: 1,
				HasGit: true, WebSearch: true,
				Loaded: map[string]bool{"terminal_send": true, "page_open": true},
			})
			contractcheck.FailErr(t, "FreezeWorkerAuthority", wErr)
			checkAuthoritySkills("worker/"+string(posture)+"/"+string(mode), wAuth.Reader.Held(), wAuth.Offered, wAuth.Schemas)

			// Timer-only wait fixture omits WaitConditions on the same offered set.
			timerSchemas := make(map[string]map[string]any, len(wAuth.Schemas))
			for k, v := range wAuth.Schemas {
				timerSchemas[k] = v
			}
			if waitMeta, hasWait := s.Schemas.ToolMeta("wait"); hasWait {
				timerSchemas["wait"] = toolschema.ProjectToolArgsSchema("wait", waitMeta.ArgsSchema, toolschema.ProjectOptions{
					PruneWaitConditions: true,
					WaitConditions:      nil,
				})
			}
			timerProf := sandbox.ToolProfile{
				ID:             "timer_only_worker",
				Tools:          map[string]bool{"command": true, "verify": true, "terminal_open": true, "wait": true, "ask_user": true},
				CommandEffects: wAuth.Reader.Effects(),
			}
			timerReader, tErr := prompts.NewReader(prompts.HostFacts{
				SessionMode: string(mode),
				Profiles:    []sandbox.ToolProfile{timerProf},
				Schemas:     s.Schemas,
				RootCount:   1,
			}, prompts.TurnProjection{
				ProfileID:        "timer_only_worker",
				Plan:             toolsurface.Compile(wAuth.Offered, nil),
				Offered:          wAuth.Offered,
				ProjectedSchemas: timerSchemas,
			})
			contractcheck.FailErr(t, "timerReader", tErr)
			checkAuthoritySkills("timer-only/"+string(posture)+"/"+string(mode), timerReader.Held(), timerReader.Offered(), timerSchemas)
		}
	}
}
