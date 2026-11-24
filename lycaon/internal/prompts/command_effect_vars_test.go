package prompts_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func toolSetMap(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func effectSet(t *testing.T, classes ...toolcontract.CommandEffectClass) toolcontract.CommandEffects {
	t.Helper()
	effects, err := toolcontract.NewCommandEffects(classes...)
	testutil.FailErr(t, "compile effects", err)
	return effects
}

func TestReaderProjectsEachCommandEffect(t *testing.T) {
	const (
		ro  = toolcontract.CommandReadOnly
		mut = toolcontract.CommandMutation
		exe = toolcontract.CommandExternalExec
		net = toolcontract.CommandNetworkEgress
	)
	for _, tc := range []struct {
		name                    string
		offered                 []string
		effects                 []toolcontract.CommandEffectClass
		mutation, exec, network bool
	}{
		{"empty set", []string{"command"}, nil, false, false, false},
		{"read only", []string{"command"}, []toolcontract.CommandEffectClass{ro}, false, false, false},
		{"mutation", []string{"command"}, []toolcontract.CommandEffectClass{ro, mut}, true, false, false},
		{"mutation and programs", []string{"command"}, []toolcontract.CommandEffectClass{ro, mut, exe}, true, true, false},
		{"network only", []string{"command"}, []toolcontract.CommandEffectClass{ro, net}, false, false, true},
		{"everything", []string{"command"}, []toolcontract.CommandEffectClass{ro, mut, exe, net}, true, true, true},
		{"command not offered", []string{"recall"}, []toolcontract.CommandEffectClass{ro, mut, exe, net}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := prompts.NewReader(prompts.ReaderInput{
				ProfileID: "worker",
				Plan:      toolsurface.Compile(tc.offered, nil),
				Profiles: []sandbox.ToolProfile{{
					ID:             "worker",
					Tools:          toolSetMap(tc.offered),
					CommandEffects: effectSet(t, tc.effects...),
				}},
			})
			testutil.FailErr(t, "new reader", err)
			vars := reader.Facts()
			for name, want := range map[string]bool{
				"profile_command_mutation":       tc.mutation,
				"profile_command_external_exec":  tc.exec,
				"profile_command_network_egress": tc.network,
				"profile_has_terminal_capture":   tc.exec,
			} {
				if got, ok := vars[name].(bool); !ok || got != want {
					t.Errorf("%s = %v, want %v", name, vars[name], want)
				}
			}
			facts := prompts.ExtractCommandGuidanceFacts(vars)
			for _, name := range []string{"profile_command_mutation", "profile_command_external_exec", "profile_command_network_egress"} {
				if facts[name] != vars[name] {
					t.Errorf("recorded fact %s = %v, variable %v", name, facts[name], vars[name])
				}
			}
		})
	}
}

func TestCommandEffectRestrictionsIntersect(t *testing.T) {
	offered := []string{"command"}
	reaches := effectSet(t, toolcontract.CommandReadOnly, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "decision_adjudicate",
		Plan:      toolsurface.Compile(offered, nil),
		Profiles: []sandbox.ToolProfile{{
			ID:             prompts.CoordinatorProfileID,
			Tools:          toolSetMap(offered),
			CommandEffects: reaches,
		}},
	})
	testutil.FailErr(t, "new reader", err)
	vars := reader.Facts()
	for _, name := range []string{"profile_command_mutation", "profile_command_external_exec", "profile_command_network_egress", "profile_has_terminal_capture"} {
		if vars[name] != false {
			t.Fatalf("%s = %v after disjoint surface and profile restrictions", name, vars[name])
		}
	}
}

func TestDeclaredReaderEffectsFollowCatalogs(t *testing.T) {
	mutation := effectSet(t, toolcontract.CommandMutation)
	programs := effectSet(t, toolcontract.CommandExternalExec)
	for scope, want := range map[string][2]bool{
		"implement_investigate": {true, true},
		"decision_adjudicate":   {true, false},
		"implement_routing":     {false, false},
		"worker_readonly":       {false, false},
	} {
		reader, err := prompts.DeclaredReader(nil, scope)
		testutil.FailErr(t, "DeclaredReader "+scope, err)
		got := reader.Effects()
		if got.Allows(mutation) != want[0] || got.Allows(programs) != want[1] {
			t.Errorf("%s effects = %v", scope, got.Classes())
		}
	}
}

func TestCommandSurfaceAndAskCoordinatorGuidanceRenders(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	roots := []projectroot.RootRef{
		{ID: "r1", Label: "app", Path: "/workspace/app", IsPrimary: true},
	}

	renderCommandSurface := func(t *testing.T, readOnly bool) string {
		t.Helper()
		offered := []string{"command", "command_output", "command_stop", "wait", "summarize", "survey_repo", "code_rewrite"}
		effects := toolcontract.AllCommandEffects()
		if readOnly {
			effects = effectSet(t, toolcontract.CommandReadOnly)
		}
		reader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: prompts.CoordinatorProfileID,
			Plan:      toolsurface.Compile(offered, nil),
			Visible:   offered,
			Profiles: []sandbox.ToolProfile{{
				ID:             prompts.CoordinatorProfileID,
				Tools:          toolSetMap(offered),
				CommandEffects: effects,
			}},
			RootCount: len(roots),
			HasGit:    true,
			WebSearch: true,
		})
		testutil.FailErr(t, "new reader", err)
		vars := prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{
			SessionScratchDir: "/scratch/sess-1",
			Posture:           gate.PostureStrict,
		})
		prompts.MergeWorkspaceRootsVars(vars, roots, roots[0].ID)
		vars, err = prompts.Vars(reader, vars)
		testutil.FailErr(t, "merge reader vars", err)
		vars["command_on_floor"] = true
		out, err := engine.Render(context.Background(), "partials/command-surface.md", vars)
		testutil.FailErr(t, "render command-surface.md", err)
		return out
	}

	readOnlySurface := renderCommandSurface(t, true)
	for _, want := range []string{
		"On this read-only surface, `command` runs read-only native utilities and pipelines only; file writes, output redirects to files, and external programs are refused.",
		"For long output, read the saved copy named in the result.",
		"One `command` call can chain orientation (`repo-map`, `outline --path`, `summarize`, `rg -n`, `find`, `git status`, `git diff`) and bounded slices (`sed -n 'A,Bp'`, `cat -n`).",
		"`&` starts a session job; `$!`/`jobs` return its handle. Join with `wait %1`/`wait $!`. Across calls, page `command_output` by cursor; follow `next_cursor`. Stop with `command_stop`. Across turns, use offered `wait` conditions. `ps`/`pgrep` inspect session processes.",
	} {
		if !strings.Contains(readOnlySurface, want) {
			t.Fatalf("read-only command-surface.md missing %q:\n%s", want, readOnlySurface)
		}
	}
	for _, forbidden := range []string{
		"multi-edit sequences",
		"replace-lines",
		"code-rewrite",
		"jq-edit",
		"heredocs",
		"kill %1",
		"kill $!",
		"`kill` signals them",
	} {
		if strings.Contains(readOnlySurface, forbidden) {
			t.Fatalf("read-only command-surface.md leaked mutating guidance %q:\n%s", forbidden, readOnlySurface)
		}
	}
	if drift := prompts.RenderedProseStructureDrift(readOnlySurface); len(drift) > 0 {
		t.Fatalf("read-only command-surface.md prose drift: %v\n%s", drift, readOnlySurface)
	}

	mutatingSurface := renderCommandSurface(t, false)
	for _, want := range []string{
		"multi-edit sequences (`edit`, `replace-lines`, `code-rewrite`, `jq-edit`, heredocs)",
		"stop with `kill %1`/`kill $!`",
		"`kill` signals them",
	} {
		if !strings.Contains(mutatingSurface, want) {
			t.Fatalf("mutating command-surface.md missing %q:\n%s", want, mutatingSurface)
		}
	}
	if drift := prompts.RenderedProseStructureDrift(mutatingSurface); len(drift) > 0 {
		t.Fatalf("mutating command-surface.md prose drift: %v\n%s", drift, mutatingSurface)
	}

	askReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		Plan:      toolsurface.Compile([]string{"command", "source_history", "task", "request_tools"}, nil),
		Visible:   []string{"command", "source_history", "task", "request_tools"},
		Profiles: []sandbox.ToolProfile{{
			ID:             prompts.CoordinatorProfileID,
			Tools:          toolSetMap([]string{"command", "source_history", "task", "request_tools"}),
			CommandEffects: effectSet(t, toolcontract.CommandReadOnly),
		}},
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "new ask coordinator reader", err)
	askVars, err := prompts.Vars(askReader, nil)
	testutil.FailErr(t, "ask coordinator vars", err)
	askCoordinator, err := engine.Render(context.Background(), "agents/coordinator-surface-ask.md", askVars)
	testutil.FailErr(t, "render coordinator-surface-ask.md", err)
	if !strings.Contains(askCoordinator, "Use `command` to read, search, or query project files and inspect Git status, diffs, and history with read-only native commands and pipelines.") {
		t.Fatalf("coordinator-surface-ask.md missing command guidance:\n%s", askCoordinator)
	}
	if drift := prompts.RenderedProseStructureDrift(askCoordinator); len(drift) > 0 {
		t.Fatalf("coordinator-surface-ask.md prose drift: %v\n%s", drift, askCoordinator)
	}
}

func TestAskCoordinatorGuidancePairedSpecificityAndOmission(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	allTools := []string{"command", "command_output", "command_stop", "source_history", "task", "request_tools", "wait", "summarize", "survey_repo"}

	renderAsk := func(t *testing.T, floor, deferred []string, rootCount int, hasGit bool) (string, string) {
		t.Helper()
		var spawnAllowlist []string
		if slices.Contains(floor, "task") {
			spawnAllowlist = []string{"repo-researcher"}
		}
		reader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:      prompts.CoordinatorProfileID,
			SurfaceID:      "observe_investigate",
			SessionMode:    string(api.SessionModeAsk),
			Plan:           toolsurface.Compile(floor, deferred),
			SpawnAllowlist: spawnAllowlist,
			Profiles: []sandbox.ToolProfile{{
				ID:             prompts.CoordinatorProfileID,
				Tools:          toolSetMap(allTools),
				CommandEffects: effectSet(t, toolcontract.CommandReadOnly),
			}},
			RootCount: rootCount,
			HasGit:    hasGit,
			WebSearch: true,
		})
		testutil.FailErr(t, "new reader", err)
		vars, err := prompts.Vars(reader, nil)
		testutil.FailErr(t, "reader vars", err)
		askOut, err := engine.Render(context.Background(), "agents/coordinator-surface-ask.md", vars)
		testutil.FailErr(t, "render coordinator-surface-ask.md", err)
		if drift := prompts.RenderedProseStructureDrift(askOut); len(drift) > 0 {
			t.Fatalf("coordinator-surface-ask.md prose drift: %v\n%s", drift, askOut)
		}
		cmdOut, err := engine.Render(context.Background(), "partials/command-surface.md", vars)
		testutil.FailErr(t, "render command-surface.md", err)
		if drift := prompts.RenderedProseStructureDrift(cmdOut); len(drift) > 0 {
			t.Fatalf("command-surface.md prose drift: %v\n%s", drift, cmdOut)
		}
		return askOut, cmdOut
	}

	// 1. Offered vs discoverable-only vs absent (varying tool reach one dimension at a time).
	floorOut, _ := renderAsk(t, []string{"command", "source_history", "task", "request_tools"}, nil, 1, true)
	for _, want := range []string{
		"Use `command` to read, search, or query project files and inspect Git status, diffs, and history with read-only native commands and pipelines.",
		"Use `source_history` to inspect recorded changes.",
		"Delegate only to read-scoped workers.",
	} {
		if !strings.Contains(floorOut, want) {
			t.Fatalf("offered floor render missing %q:\n%s", want, floorOut)
		}
	}

	deferredOut, _ := renderAsk(t, []string{"command", "source_history", "request_tools"}, []string{"task"}, 1, true)
	if !strings.Contains(deferredOut, "When delegating via `task` (loaded with `request_tools`), assign only read-scoped workers.") {
		t.Fatalf("discoverable-only render missing deferred task guidance:\n%s", deferredOut)
	}
	if strings.Contains(deferredOut, "Delegate only to read-scoped workers.") {
		t.Fatalf("discoverable-only render claimed offered floor task:\n%s", deferredOut)
	}

	noFolderOut, _ := renderAsk(t, []string{"wait"}, nil, 0, false)
	for _, forbidden := range []string{"`command`", "`source_history`", "`task`", "Git status"} {
		if strings.Contains(noFolderOut, forbidden) {
			t.Fatalf("no-folder render leaked unreachable %q:\n%s", forbidden, noFolderOut)
		}
	}

	// 2. Single Git root vs non-Git root vs multi-root (varying only has_git / root_count).
	nonGitAsk, nonGitCmd := renderAsk(t, []string{"command", "source_history", "task"}, nil, 1, false)
	if !strings.Contains(nonGitAsk, "Use `command` to read, search, or query project files with read-only native commands and pipelines.") {
		t.Fatalf("non-Git Ask render missing file-read guidance:\n%s", nonGitAsk)
	}
	for _, forbidden := range []string{"Git status", "git status", "git diff"} {
		if strings.Contains(nonGitAsk, forbidden) || strings.Contains(nonGitCmd, forbidden) {
			t.Fatalf("non-Git render leaked Git guidance %q:\nask=%s\ncmd=%s", forbidden, nonGitAsk, nonGitCmd)
		}
	}

	multiGitAsk, multiGitCmd := renderAsk(t, []string{"command", "source_history", "task"}, nil, 2, true)
	if !strings.Contains(multiGitAsk, "in Git-backed folders") {
		t.Fatalf("multi-root Git Ask render missing multi-root qualification:\n%s", multiGitAsk)
	}
	if !strings.Contains(multiGitCmd, "in Git folders") {
		t.Fatalf("multi-root Git command-surface.md missing multi-root qualification:\n%s", multiGitCmd)
	}
}
