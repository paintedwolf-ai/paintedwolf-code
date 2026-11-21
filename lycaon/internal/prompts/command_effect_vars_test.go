package prompts_test

import (
	"context"
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

	askCoordinator, err := engine.Render(context.Background(), "agents/coordinator-surface-ask.md", nil)
	testutil.FailErr(t, "render coordinator-surface-ask.md", err)
	if !strings.Contains(askCoordinator, "Use `command` to read, search, or query project files and inspect Git status, diffs, and history with read-only native commands and pipelines.") {
		t.Fatalf("coordinator-surface-ask.md missing command guidance:\n%s", askCoordinator)
	}
	if strings.Contains(askCoordinator, "inspect_source") {
		t.Fatalf("coordinator-surface-ask.md still references inspect_source:\n%s", askCoordinator)
	}
}
