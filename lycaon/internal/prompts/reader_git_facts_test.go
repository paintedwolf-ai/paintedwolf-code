package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// Git and workspace reader facts follow the profile's command effects and
// per-operation denies, so guidance guarded by them follows the profile too.
func TestReaderGitFactsFollowProfileDenies(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	readOnly, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "read-only effects", err)
	readMut, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	testutil.FailErr(t, "read and mutation effects", err)
	dispatchPlan, err := prompts.CompileSurfacePlan("implement_dispatch", 1)
	testutil.FailErr(t, "CompileSurfacePlan implement_dispatch", err)

	type readerCase struct {
		host prompts.HostFacts
		turn prompts.TurnProjection
	}
	custom := func(id string, tools []string, deferred []string, effects toolcontract.CommandEffects, deny ...string) readerCase {
		toolSet := map[string]bool{}
		for _, name := range append(append([]string{}, tools...), deferred...) {
			toolSet[name] = true
		}
		deferredSet := map[string]bool{}
		for _, name := range deferred {
			deferredSet[name] = true
		}
		return readerCase{
			host: prompts.HostFacts{
				Profiles: []sandbox.ToolProfile{{
					ID: id, Tools: toolSet, DeferredTools: deferredSet, CommandEffects: effects, DenyTools: deny,
				}},
				RootCount: 1,
				HasGit:    true,
			},
			turn: prompts.TurnProjection{ProfileID: id, Plan: toolsurface.Compile(tools, deferred), Offered: tools},
		}
	}
	stock := func(id string) readerCase {
		return readerCase{
			host: prompts.HostFacts{Profiles: profiles, RootCount: 1, HasGit: true, HasAnyGitRoot: true},
			turn: prompts.TurnProjection{ProfileID: id, Declared: true},
		}
	}

	for _, tc := range []struct {
		name  string
		input readerCase
		want  map[string]any
	}{
		{"stock plan_write_only", stock("plan_write_only"),
			map[string]any{"can_git_commit": true}},
		{"stock editor_file denies git_commit", stock("editor_file"),
			map[string]any{"profile_command_mutation": true, "can_git_read": true, "can_git_commit": false}},
		{"deferred command denying git_commit", custom("deferred_no_commit", []string{"request_tools"}, []string{"command"}, readMut, "git_commit"),
			map[string]any{"can_request_git_commit": false, "can_request_git_read": true}},
		{"read-only command denying git_blame", custom("ro_no_blame", []string{"command", "source_history"}, nil, readOnly, "git_blame"),
			map[string]any{"can_git_read": false}},
		{"external exec denying git_*", custom("exec_no_git", []string{"command"}, nil, toolcontract.AllCommandEffects(), "git_*"),
			map[string]any{"can_git_bisect": false}},
		{"coordinator dispatch with one root", readerCase{
			host: prompts.HostFacts{
				Profiles: profiles, RootCount: 1, HasGit: true, HasAnyGitRoot: true, WebSearch: true, SpawnAllowlist: spawn.AmbientAllowedAgents(),
			},
			turn: prompts.TurnProjection{
				ProfileID: prompts.CoordinatorProfileID, SurfaceID: "implement_dispatch", Plan: dispatchPlan, Offered: dispatchPlan.ImmediateNames(),
			},
		}, map[string]any{"has_workspace_roots": true, "has_file_tools": false}},
	} {
		reader, err := prompts.NewReader(tc.input.host, tc.input.turn)
		testutil.FailErr(t, "NewReader "+tc.name, err)
		facts := reader.Facts()
		for fact, want := range tc.want {
			if facts[fact] != want {
				t.Errorf("%s: %s = %v, want %v", tc.name, fact, facts[fact], want)
			}
		}
	}
}
