package prompts_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func loadSkillByName(t *testing.T, eff *extpacks.EffectiveCatalog, name string) skills.Skill {
	t.Helper()
	loaded, _ := extpacks.LoadEffectiveSkills(eff)
	for _, sk := range loaded {
		if sk.Name == name {
			return sk
		}
	}
	t.Fatalf("skill %q not found in stock catalog", name)
	return skills.Skill{}
}

func renderSkillForReader(t *testing.T, sk skills.Skill, reader prompts.Reader) string {
	t.Helper()
	vars, err := prompts.SkillVars(reader)
	testutil.FailErr(t, "SkillVars "+sk.Name, err)
	tpl, err := pongoplain.Compile(sk.Body)
	testutil.FailErr(t, "Compile "+sk.Name, err)
	out, err := pongoplain.Execute(t.Context(), tpl, vars)
	testutil.FailErr(t, "Execute "+sk.Name, err)
	return out
}

func newGuidanceTestEnv(t *testing.T) (
	*prompts.FileTemplateEngine,
	*extpacks.EffectiveCatalog,
	[]sandbox.ToolProfile,
	toolcontract.CommandEffects,
) {
	t.Helper()
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	readOnlyEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "NewCommandEffects read_only", err)
	return engine, eff, profiles, readOnlyEffects
}

func TestStateSpecificGuidanceValidationExecution(t *testing.T) {
	engine, _, _, readOnlyEffects := newGuidanceTestEnv(t)

	withCmdExec, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "worker_verify",
		Profiles: []sandbox.ToolProfile{{
			ID:             "worker_verify",
			Tools:          map[string]bool{"command": true, "complete_leg": true},
			CommandEffects: toolcontract.AllCommandEffects(),
		}},
		Plan:      toolsurface.Compile([]string{"command", "complete_leg"}, nil),
		Visible:   []string{"command", "complete_leg"},
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "withCmdExec", err)
	varsExec, err := prompts.Vars(withCmdExec, map[string]any{"verify_command": "./task test:short"})
	testutil.FailErr(t, "Vars withCmdExec", err)
	outExec, err := engine.Render(t.Context(), "partials/validation-execution.md", varsExec)
	testutil.FailErr(t, "Render validation-execution exec", err)
	if !strings.Contains(outExec, `command(command: "./task test:short", verification: true)`) || !strings.Contains(outExec, "command(verification: true)") {
		t.Fatalf("eligible verify state lost concrete command(verification: true):\n%s", outExec)
	}

	loadableCmd, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "worker_loadable",
		Profiles: []sandbox.ToolProfile{{
			ID:             "worker_loadable",
			Tools:          map[string]bool{"command": true, "request_tools": true, "complete_leg": true},
			DeferredTools:  map[string]bool{"command": true},
			CommandEffects: toolcontract.AllCommandEffects(),
		}},
		Plan:      toolsurface.Compile([]string{"request_tools", "complete_leg"}, []string{"command"}),
		Visible:   []string{"request_tools", "complete_leg", "command"},
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "loadableCmd", err)
	varsLoad, err := prompts.Vars(loadableCmd, map[string]any{"verify_command": "./task test:short"})
	testutil.FailErr(t, "Vars loadableCmd", err)
	outLoad, err := engine.Render(t.Context(), "partials/validation-execution.md", varsLoad)
	testutil.FailErr(t, "Render validation-execution load", err)
	if !strings.Contains(outLoad, `request_tools(need: "run a verification command")`) || strings.Contains(outLoad, "command(verification: true)") {
		t.Fatalf("loadable verify state did not direct request_tools without premature command call:\n%s", outLoad)
	}

	readOnlyCmd, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "worker_ro",
		Profiles: []sandbox.ToolProfile{{
			ID:             "worker_ro",
			Tools:          map[string]bool{"command": true, "complete_leg": true},
			CommandEffects: readOnlyEffects,
		}},
		Plan:      toolsurface.Compile([]string{"command", "complete_leg"}, nil),
		Visible:   []string{"command", "complete_leg"},
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "readOnlyCmd", err)
	varsRO, err := prompts.Vars(readOnlyCmd, map[string]any{"verify_command": "./task test:short"})
	testutil.FailErr(t, "Vars readOnlyCmd", err)
	outRO, err := engine.Render(t.Context(), "partials/validation-execution.md", varsRO)
	testutil.FailErr(t, "Render validation-execution ro", err)
	if strings.Contains(outRO, "verification: true") || strings.Contains(outRO, "request_tools(") || !strings.Contains(outRO, "Inspect what you can") {
		t.Fatalf("read-only state leaked unavailable verification arg or loader:\n%s", outRO)
	}
}

func TestStateSpecificGuidanceGitHistoryCommitAndBisect(t *testing.T) {
	_, eff, profiles, readOnlyEffects := newGuidanceTestEnv(t)

	t.Run("investigate_code_history_paired_states", func(t *testing.T) {
		sk := loadSkillByName(t, eff, "investigate-code-history")

		gitPrimary, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:     "explore_readonly",
			Profiles:      profiles,
			Visible:       []string{"command", "source_history"},
			Loaded:        map[string]bool{"command": true, "source_history": true},
			RootCount:     1,
			HasGit:        true,
			HasAnyGitRoot: true,
			WebSearch:     true,
		})
		testutil.FailErr(t, "gitPrimary", err)
		outPrimary := renderSkillForReader(t, sk, gitPrimary)
		for _, want := range []string{"git status --short", "git diff -- paths", "git blame -L A,B -- path"} {
			if !strings.Contains(outPrimary, want) {
				t.Fatalf("can_git_read=true missing %q:\n%s", want, outPrimary)
			}
		}

		// Secondary root has Git while primary does not.
		secondaryGit, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:     "explore_readonly",
			Profiles:      profiles,
			Visible:       []string{"command", "source_history"},
			Loaded:        map[string]bool{"command": true, "source_history": true},
			RootCount:     2,
			HasGit:        false,
			HasAnyGitRoot: true,
			WebSearch:     true,
		})
		testutil.FailErr(t, "secondaryGit", err)
		outSecondary := renderSkillForReader(t, sk, secondaryGit)
		if !strings.Contains(outSecondary, `cwd="@<label>"`) || strings.Contains(outSecondary, "git blame -L A,B") {
			t.Fatalf("secondary-git state did not guide cwd=@<label> while omitting primary git blame:\n%s", outSecondary)
		}

		deferredGit, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "worker_loadable_git",
			Profiles: []sandbox.ToolProfile{{
				ID:             "worker_loadable_git",
				Tools:          map[string]bool{"command": true, "request_tools": true},
				DeferredTools:  map[string]bool{"command": true},
				CommandEffects: readOnlyEffects,
			}},
			Plan:          toolsurface.Compile([]string{"request_tools"}, []string{"command"}),
			Visible:       []string{"request_tools", "command"},
			RootCount:     1,
			HasGit:        true,
			HasAnyGitRoot: true,
			WebSearch:     true,
		})
		testutil.FailErr(t, "deferredGit", err)
		outDeferred := renderSkillForReader(t, sk, deferredGit)
		if !strings.Contains(outDeferred, `request_tools(need: "inspect git history")`) || strings.Contains(outDeferred, "git blame") {
			t.Fatalf("deferred command state did not render request_tools:\n%s", outDeferred)
		}

		workerBranch, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:      "implement",
			Profiles:       profiles,
			Visible:        []string{"command", "source_history"},
			Loaded:         map[string]bool{"command": true, "source_history": true},
			RootCount:      1,
			HasGit:         true,
			HasAnyGitRoot:  true,
			OnWorkerBranch: true,
			WebSearch:      true,
		})
		testutil.FailErr(t, "workerBranch", err)
		outBranch := renderSkillForReader(t, sk, workerBranch)
		if !strings.Contains(outBranch, "source_history(path, mode=lines, start_line, end_line)") || strings.Contains(outBranch, "git blame") {
			t.Fatalf("worker branch state did not route to source_history:\n%s", outBranch)
		}
	})

	t.Run("commit_and_bisect_paired_states", func(t *testing.T) {
		commitSk := loadSkillByName(t, eff, "commit-in-logical-groups")
		bisectSk := loadSkillByName(t, eff, "hunt-a-regression-with-bisect")

		buildPlan, err := prompts.CompileSurfacePlan("implement_investigate", 1)
		testutil.FailErr(t, "CompileSurfacePlan implement_investigate", err)
		mutatingCoord, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:      prompts.CoordinatorProfileID,
			SurfaceID:      "implement_investigate",
			Profiles:       profiles,
			Plan:           buildPlan,
			RootCount:      1,
			HasGit:         true,
			HasAnyGitRoot:  true,
			WebSearch:      true,
			SpawnAllowlist: spawn.AmbientAllowedAgents(),
		})
		testutil.FailErr(t, "mutatingCoord", err)
		commitMut := renderSkillForReader(t, commitSk, mutatingCoord)
		if !strings.Contains(commitMut, "git add -- PATHS") || !strings.Contains(commitMut, "git commit -m MESSAGE -- PATHS") {
			t.Fatalf("can_git_commit=true lost git add/commit commands:\n%s", commitMut)
		}
		bisectMut := renderSkillForReader(t, bisectSk, mutatingCoord)
		if !strings.Contains(bisectMut, "git bisect start") || !strings.Contains(bisectMut, "git worktree add @scratch/bisect HEAD") {
			t.Fatalf("can_git_bisect=true lost git bisect/worktree commands:\n%s", bisectMut)
		}

		routingPlan, err := prompts.CompileSurfacePlan("implement_routing", 1)
		testutil.FailErr(t, "CompileSurfacePlan implement_routing", err)
		readOnlyCoord, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:      prompts.CoordinatorProfileID,
			SurfaceID:      "implement_routing",
			Profiles:       profiles,
			Plan:           routingPlan,
			RootCount:      1,
			HasGit:         true,
			HasAnyGitRoot:  true,
			WebSearch:      true,
			SpawnAllowlist: spawn.AmbientAllowedAgents(),
		})
		testutil.FailErr(t, "readOnlyCoord", err)
		commitRO := renderSkillForReader(t, commitSk, readOnlyCoord)
		if !strings.Contains(commitRO, "git status --short") || strings.Contains(commitRO, "git add -- PATHS") {
			t.Fatalf("read-only git state should keep git status inventory and omit git add:\n%s", commitRO)
		}
		bisectRO := renderSkillForReader(t, bisectSk, readOnlyCoord)
		if strings.Contains(bisectRO, "git bisect start") || !strings.Contains(bisectRO, "125") {
			t.Fatalf("read-only bisect state leaked git bisect start or lost exit-125 script spec:\n%s", bisectRO)
		}
	})
}

func TestStateSpecificGuidanceCoordinatorAndReleaseStates(t *testing.T) {
	engine, eff, profiles, readOnlyEffects := newGuidanceTestEnv(t)

	t.Run("coordinator_dispatch_ask_and_posture_paired_states", func(t *testing.T) {
		dispatch1Plan, err := prompts.CompileSurfacePlan("implement_dispatch", 1)
		testutil.FailErr(t, "CompileSurfacePlan dispatch 1", err)
		dispatch1Reader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:      prompts.CoordinatorProfileID,
			SurfaceID:      "implement_dispatch",
			Profiles:       profiles,
			Plan:           dispatch1Plan,
			RootCount:      1,
			HasGit:         true,
			HasAnyGitRoot:  true,
			WebSearch:      true,
			SpawnAllowlist: spawn.AmbientAllowedAgents(),
		})
		testutil.FailErr(t, "dispatch1Reader", err)
		f1 := dispatch1Reader.Facts()
		if f1["has_workspace_roots"] != true || f1["has_file_tools"] != false {
			t.Fatalf("dispatch at 1 root expected has_workspace_roots=true has_file_tools=false, got %v / %v",
				f1["has_workspace_roots"], f1["has_file_tools"])
		}
		vars1, err := prompts.Vars(dispatch1Reader, nil)
		testutil.FailErr(t, "Vars dispatch1", err)
		outDispatch1, err := engine.Render(t.Context(), "agents/coordinator-mode-implement-dispatch.md", vars1)
		testutil.FailErr(t, "Render dispatch 1", err)
		if !strings.Contains(outDispatch1, "scope.mode: write") {
			t.Fatalf("dispatch at 1 root lost write-worker dispatch guidance:\n%s", outDispatch1)
		}

		dispatch0Plan, err := prompts.CompileSurfacePlan("implement_dispatch", 0)
		testutil.FailErr(t, "CompileSurfacePlan dispatch 0", err)
		dispatch0Reader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:      prompts.CoordinatorProfileID,
			SurfaceID:      "implement_dispatch",
			Profiles:       profiles,
			Plan:           dispatch0Plan,
			RootCount:      0,
			WebSearch:      true,
			SpawnAllowlist: []string{"web-researcher"},
		})
		testutil.FailErr(t, "dispatch0Reader", err)
		vars0, err := prompts.Vars(dispatch0Reader, nil)
		testutil.FailErr(t, "Vars dispatch0", err)
		outDispatch0, err := engine.Render(t.Context(), "agents/coordinator-mode-implement-dispatch.md", vars0)
		testutil.FailErr(t, "Render dispatch 0", err)
		if !strings.Contains(outDispatch0, "web-researcher") || strings.Contains(outDispatch0, "scope.mode: write") {
			t.Fatalf("dispatch at 0 roots did not narrow to web-researcher:\n%s", outDispatch0)
		}

		obsPlan, err := prompts.CompileSurfacePlan("observe_investigate", 1)
		testutil.FailErr(t, "CompileSurfacePlan observe_investigate", err)
		askReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:   prompts.CoordinatorProfileID,
			SurfaceID:   "observe_investigate",
			SessionMode: "ask",
			Profiles:    profiles,
			Plan:        obsPlan,
			RootCount:   1,
			WebSearch:   true,
		})
		testutil.FailErr(t, "askReader", err)
		askVars, err := prompts.Vars(askReader, nil)
		testutil.FailErr(t, "Vars askReader", err)
		outAsk, err := engine.Render(t.Context(), "agents/coordinator-mode-observe-investigate.md", askVars)
		testutil.FailErr(t, "Render observe Ask", err)
		if !strings.Contains(outAsk, "Read-only turn (Ask mode).") {
			t.Fatalf("Ask mode observe prompt missing (Ask mode):\n%s", outAsk)
		}

		agentReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:   prompts.CoordinatorProfileID,
			SurfaceID:   "observe_investigate",
			SessionMode: "agent",
			Profiles:    profiles,
			Plan:        obsPlan,
			RootCount:   1,
			WebSearch:   true,
		})
		testutil.FailErr(t, "agentReader", err)
		agentVars, err := prompts.Vars(agentReader, nil)
		testutil.FailErr(t, "Vars agentReader", err)
		outAgent, err := engine.Render(t.Context(), "agents/coordinator-mode-observe-investigate.md", agentVars)
		testutil.FailErr(t, "Render observe Agent", err)
		if strings.Contains(outAgent, "(Ask mode)") || !strings.Contains(outAgent, "Read-only turn.") {
			t.Fatalf("Agent mode observe prompt mislabeled Ask mode:\n%s", outAgent)
		}
	})

	t.Run("prepare_a_release_fixed_workspace_varying_reach", func(t *testing.T) {
		relSk := loadSkillByName(t, eff, "prepare-a-release")

		fullExec, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "rel_full",
			Profiles: []sandbox.ToolProfile{{
				ID:             "rel_full",
				Tools:          map[string]bool{"command": true, "verify": true},
				CommandEffects: toolcontract.AllCommandEffects(),
			}},
			Plan:              toolsurface.Compile([]string{"command", "verify"}, nil),
			Visible:           []string{"command", "verify"},
			RootCount:         1,
			HasGit:            true,
			HasAnyGitRoot:     true,
			SessionScratchDir: "/scratch/rel",
		})
		testutil.FailErr(t, "fullExec", err)
		outFull := renderSkillForReader(t, relSk, fullExec)
		for _, want := range []string{"git log previous..release", "git status --short", "with `verify`", "@scratch/<version>/"} {
			if !strings.Contains(outFull, want) {
				t.Fatalf("full release state missing %q:\n%s", want, outFull)
			}
		}

		verifyOnlyDeferredCmd, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "rel_verify_only",
			Profiles: []sandbox.ToolProfile{{
				ID:             "rel_verify_only",
				Tools:          map[string]bool{"command": true, "verify": true, "request_tools": true},
				DeferredTools:  map[string]bool{"command": true},
				CommandEffects: toolcontract.AllCommandEffects(),
			}},
			Plan:              toolsurface.Compile([]string{"verify", "request_tools"}, []string{"command"}),
			Visible:           []string{"verify", "request_tools", "command"},
			RootCount:         1,
			HasGit:            true,
			HasAnyGitRoot:     true,
			SessionScratchDir: "/scratch/rel",
		})
		testutil.FailErr(t, "verifyOnlyDeferredCmd", err)
		outVerifyOnly := renderSkillForReader(t, relSk, verifyOnlyDeferredCmd)
		if !strings.Contains(outVerifyOnly, `request_tools(need="command")`) || !strings.Contains(outVerifyOnly, "with `verify`") || strings.Contains(outVerifyOnly, "in `command`") {
			t.Fatalf("verify-only partial state leaked unoffered command or missed request_tools:\n%s", outVerifyOnly)
		}

		roCmdOnly, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "rel_ro_cmd",
			Profiles: []sandbox.ToolProfile{{
				ID:             "rel_ro_cmd",
				Tools:          map[string]bool{"command": true},
				CommandEffects: readOnlyEffects,
			}},
			Plan:              toolsurface.Compile([]string{"command"}, nil),
			Visible:           []string{"command"},
			RootCount:         1,
			HasGit:            true,
			HasAnyGitRoot:     true,
			SessionScratchDir: "/scratch/rel",
		})
		testutil.FailErr(t, "roCmdOnly", err)
		outROCmd := renderSkillForReader(t, relSk, roCmdOnly)
		if !strings.Contains(outROCmd, "git log previous..release") || strings.Contains(outROCmd, "`verify`") || strings.Contains(outROCmd, "verification: true") || !strings.Contains(outROCmd, "unexecuted checks") {
			t.Fatalf("read-only command state leaked verify/verification or lost git log:\n%s", outROCmd)
		}
	})
}

func TestStateSpecificGuidanceProjectedSchemaAndSelectorStates(t *testing.T) {
	engine, eff, _, readOnlyEffects := newGuidanceTestEnv(t)
	{
		sampleResources := []prompts.AgentHostResourceView{{
			ID: "docker", Label: "Docker", Category: "containers", Status: "ready", Access: "ask",
		}}
		occHost := prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{HostResources: sampleResources})

		// Read-only command is pruned while verify keeps runner fields.
		roCmdPlusVerify, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "implement",
			Profiles: []sandbox.ToolProfile{{
				ID:             "implement",
				Tools:          map[string]bool{"command": true, "verify": true, "secret_generate": true, "secret_list": true, "secret_revoke": true},
				CommandEffects: readOnlyEffects,
			}},
			Plan:              toolsurface.Compile([]string{"command", "verify", "secret_generate", "secret_list", "secret_revoke"}, nil),
			Visible:           []string{"command", "verify", "secret_generate", "secret_list", "secret_revoke"},
			RootCount:         1,
			HasGit:            true,
			SessionScratchDir: "/scratch/s1",
		})
		testutil.FailErr(t, "roCmdPlusVerify", err)
		varsROVerify, err := prompts.Vars(roCmdPlusVerify, occHost)
		testutil.FailErr(t, "Vars roCmdPlusVerify", err)

		hostOut, err := engine.Render(t.Context(), "partials/host-resources.md", varsROVerify)
		testutil.FailErr(t, "Render host-resources ro+verify", err)
		if !strings.Contains(hostOut, "with `verify`") || strings.Contains(hostOut, "`command`") {
			t.Fatalf("host-resources with pruned command + verify must name only verify:\n%s", hostOut)
		}

		secretOut, err := engine.Render(t.Context(), "units/managed-secret-tools.md", varsROVerify)
		testutil.FailErr(t, "Render managed-secret-tools ro+verify", err)
		if !strings.Contains(secretOut, "on `verify`") || strings.Contains(secretOut, "`command`") {
			t.Fatalf("managed-secret-tools with pruned command + verify must name only verify:\n%s", secretOut)
		}

		sandboxOut, err := engine.Render(t.Context(), "partials/sandbox-confinement.md", varsROVerify)
		testutil.FailErr(t, "Render sandbox-confinement ro+verify", err)
		if !strings.Contains(sandboxOut, "on `verify`") || strings.Contains(sandboxOut, "`command`") || strings.Contains(sandboxOut, "Deliver files in attached folders") {
			t.Fatalf("sandbox-confinement on read-only clamped implement profile leaked command or file-write claims:\n%s", sandboxOut)
		}

		roCmdAlone, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "explore_readonly",
			Profiles: []sandbox.ToolProfile{{
				ID:             "explore_readonly",
				Tools:          map[string]bool{"command": true, "secret_list": true},
				CommandEffects: readOnlyEffects,
			}},
			Plan:      toolsurface.Compile([]string{"command", "secret_list"}, nil),
			Visible:   []string{"command", "secret_list"},
			RootCount: 1,
			HasGit:    true,
		})
		testutil.FailErr(t, "roCmdAlone", err)
		varsROAlone, err := prompts.Vars(roCmdAlone, occHost)
		testutil.FailErr(t, "Vars roCmdAlone", err)
		hostOutRO, err := engine.Render(t.Context(), "partials/host-resources.md", varsROAlone)
		testutil.FailErr(t, "Render host-resources ro", err)
		if strings.Contains(hostOutRO, "capability_request") {
			t.Fatalf("read-only command leaked capability_request in host-resources:\n%s", hostOutRO)
		}
		sandboxOutRO, err := engine.Render(t.Context(), "partials/sandbox-confinement.md", varsROAlone)
		testutil.FailErr(t, "Render sandbox-confinement ro", err)
		if strings.Contains(sandboxOutRO, "capability_request") || !strings.Contains(sandboxOutRO, "cannot allow writes or external programs") {
			t.Fatalf("read-only sandbox-confinement missed read-only limitation:\n%s", sandboxOutRO)
		}

		perfSk := loadSkillByName(t, eff, "diagnose-performance")
		rawExhaustion, err := perfSk.ReadResource("references/diagnose_resource_exhaustion.md")
		testutil.FailErr(t, "ReadResource diagnose_resource_exhaustion.md", err)
		exhaustionTpl, err := pongoplain.Compile(string(rawExhaustion))
		testutil.FailErr(t, "Compile diagnose_resource_exhaustion.md", err)

		for _, hidden := range []bool{false, true} {
			selReader, rErr := prompts.NewReader(prompts.ReaderInput{
				ProfileID: "worker_ext",
				Profiles: []sandbox.ToolProfile{{
					ID:             "worker_ext",
					Tools:          map[string]bool{"command": true},
					CommandEffects: toolcontract.AllCommandEffects(),
				}},
				Plan:                  toolsurface.Compile([]string{"command"}, nil),
				Visible:               []string{"command"},
				RootCount:             1,
				HasGit:                true,
				CommandExternalHidden: hidden,
			})
			testutil.FailErr(t, "selReader", rErr)
			skVars, vErr := prompts.SkillVars(selReader)
			testutil.FailErr(t, "SkillVars selReader", vErr)
			exOut, exErr := pongoplain.Execute(t.Context(), exhaustionTpl, skVars)
			testutil.FailErr(t, "Execute exhaustion", exErr)
			uVars, uErr := prompts.Vars(selReader, nil)
			testutil.FailErr(t, "Vars selReader", uErr)
			gitUnitOut, gErr := engine.Render(t.Context(), "units/git-mutation-user-consent.md", uVars)
			testutil.FailErr(t, "Render git-mutation-user-consent", gErr)

			if hidden {
				if strings.Contains(exOut, "external -- lsof") || strings.Contains(gitUnitOut, "external -- git") {
					t.Fatalf("CommandExternalHidden=true leaked external -- selector:\nexhaustion:\n%s\ngit:\n%s", exOut, gitUnitOut)
				}
			} else {
				if !strings.Contains(exOut, "external -- lsof +L1") || !strings.Contains(gitUnitOut, "external -- git") {
					t.Fatalf("CommandExternalHidden=false omitted concrete external -- selector:\nexhaustion:\n%s\ngit:\n%s", exOut, gitUnitOut)
				}
			}
		}
	}
}

func TestStateSpecificGuidancePerOperationGitReachWitnesses(t *testing.T) {
	_, eff, profiles, readOnlyEffects := newGuidanceTestEnv(t)
	readMutEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	testutil.FailErr(t, "NewCommandEffects read_only+mutation", err)

	t.Run("commit_skill_holds_git_command_and_mutation_constant_while_denying_git_commit", func(t *testing.T) {
		commitSk := loadSkillByName(t, eff, "commit-in-logical-groups")
		rawBatch, rErr := commitSk.ReadResource("references/batch-and-amend.md")
		testutil.FailErr(t, "ReadResource batch-and-amend.md", rErr)
		batchTpl, cErr := pongoplain.Compile(string(rawBatch))
		testutil.FailErr(t, "Compile batch-and-amend.md", cErr)

		allowCommitReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:     "plan_write_only",
			Profiles:      profiles,
			RootCount:     1,
			HasGit:        true,
			HasAnyGitRoot: true,
		})
		testutil.FailErr(t, "allowCommitReader", err)
		if allowCommitReader.Facts()["can_git_commit"] != true {
			t.Fatalf("plan_write_only with Git root should have can_git_commit=true, got %v", allowCommitReader.Facts()["can_git_commit"])
		}
		outAllow := renderSkillForReader(t, commitSk, allowCommitReader)
		if !strings.Contains(outAllow, "git add -- PATHS") || !strings.Contains(outAllow, "git commit -m MESSAGE -- PATHS") {
			t.Fatalf("eligible commit profile missing git add / git commit:\n%s", outAllow)
		}

		// Stock editor_file mutates and offers command on Git, while denying git_commit.
		editorReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:     "editor_file",
			Profiles:      profiles,
			RootCount:     1,
			HasGit:        true,
			HasAnyGitRoot: true,
		})
		testutil.FailErr(t, "editorReader", err)
		facts := editorReader.Facts()
		if facts["profile_command_mutation"] != true || facts["can_git_read"] != true || facts["can_git_commit"] != false {
			t.Fatalf("editor_file facts mismatch: mutation=%v can_git_read=%v can_git_commit=%v",
				facts["profile_command_mutation"], facts["can_git_read"], facts["can_git_commit"])
		}
		outDeny := renderSkillForReader(t, commitSk, editorReader)
		if strings.Contains(outDeny, "git commit") || strings.Contains(outDeny, "git add") {
			t.Fatalf("editor_file (git_commit denied) leaked git commit/add:\n%s", outDeny)
		}
		if !strings.Contains(outDeny, "git status --short") || !strings.Contains(outDeny, "report its paths and message to the coordinator") {
			t.Fatalf("editor_file lost git status survey or coordinator commit handoff:\n%s", outDeny)
		}
		denyVars, err := prompts.SkillVars(editorReader)
		testutil.FailErr(t, "SkillVars editorReader", err)
		batchOut, err := pongoplain.Execute(t.Context(), batchTpl, denyVars)
		testutil.FailErr(t, "Execute batch-and-amend editorReader", err)
		if strings.Contains(batchOut, "git commit") || !strings.Contains(batchOut, "report each group's paths and message to the coordinator") {
			t.Fatalf("batch-and-amend leaked git commit or missed handoff on editor_file:\n%s", batchOut)
		}

		deferredDenyCommit, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "deferred_no_commit",
			Profiles: []sandbox.ToolProfile{{
				ID:             "deferred_no_commit",
				Tools:          map[string]bool{"command": true, "request_tools": true},
				DeferredTools:  map[string]bool{"command": true},
				CommandEffects: readMutEffects,
				DenyTools:      []string{"git_commit"},
			}},
			Plan:      toolsurface.Compile([]string{"request_tools"}, []string{"command"}),
			Visible:   []string{"request_tools", "command"},
			RootCount: 1,
			HasGit:    true,
		})
		testutil.FailErr(t, "deferredDenyCommit", err)
		df := deferredDenyCommit.Facts()
		if df["can_request_git_commit"] != false || df["can_request_git_read"] != true {
			t.Fatalf("deferred git_commit deny: can_request_git_commit=%v (want false), can_request_git_read=%v (want true)",
				df["can_request_git_commit"], df["can_request_git_read"])
		}
	})

	t.Run("read_and_bisect_skills_respect_per_form_operation_denies", func(t *testing.T) {
		histSk := loadSkillByName(t, eff, "investigate-code-history")
		bisectSk := loadSkillByName(t, eff, "hunt-a-regression-with-bisect")

		denyBlameReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "ro_no_blame",
			Profiles: []sandbox.ToolProfile{{
				ID:             "ro_no_blame",
				Tools:          map[string]bool{"command": true, "source_history": true},
				CommandEffects: readOnlyEffects,
				DenyTools:      []string{"git_blame"},
			}},
			Plan:      toolsurface.Compile([]string{"command", "source_history"}, nil),
			Visible:   []string{"command", "source_history"},
			RootCount: 1,
			HasGit:    true,
		})
		testutil.FailErr(t, "denyBlameReader", err)
		if denyBlameReader.Facts()["can_git_read"] != false {
			t.Fatalf("denying git_blame must set can_git_read=false, got %v", denyBlameReader.Facts()["can_git_read"])
		}
		histOut := renderSkillForReader(t, histSk, denyBlameReader)
		if strings.Contains(histOut, "git blame") || !strings.Contains(histOut, "source_history") {
			t.Fatalf("git_blame-denied history render leaked git blame or lost source_history fallback:\n%s", histOut)
		}

		denyBisectReader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: "exec_no_git",
			Profiles: []sandbox.ToolProfile{{
				ID:             "exec_no_git",
				Tools:          map[string]bool{"command": true},
				CommandEffects: toolcontract.AllCommandEffects(),
				DenyTools:      []string{"git_*"},
			}},
			Plan:      toolsurface.Compile([]string{"command"}, nil),
			Visible:   []string{"command"},
			RootCount: 1,
			HasGit:    true,
		})
		testutil.FailErr(t, "denyBisectReader", err)
		if denyBisectReader.Facts()["can_git_bisect"] != false {
			t.Fatalf("denying git_* on external-exec profile must set can_git_bisect=false, got %v", denyBisectReader.Facts()["can_git_bisect"])
		}
		bisectOut := renderSkillForReader(t, bisectSk, denyBisectReader)
		if strings.Contains(bisectOut, "git bisect start") || !strings.Contains(bisectOut, "for the coordinator to run the bisect") {
			t.Fatalf("git-denied bisect render leaked git bisect start or missed handoff:\n%s", bisectOut)
		}
	})
}
