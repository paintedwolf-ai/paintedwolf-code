package prompts_test

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestNewReaderRefusesIncompleteInput(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	plan := toolsurface.Compile([]string{"command"}, nil)

	for _, tc := range []struct {
		name string
		host prompts.HostFacts
		turn prompts.TurnProjection
	}{
		{
			name: "nil profiles",
			turn: prompts.TurnProjection{ProfileID: "implement", Plan: plan},
		},
		{
			name: "empty profile ID",
			host: prompts.HostFacts{Profiles: profiles},
			turn: prompts.TurnProjection{Plan: plan},
		},
		{
			name: "unknown profile ID",
			host: prompts.HostFacts{Profiles: profiles},
			turn: prompts.TurnProjection{ProfileID: "unknown_profile", Plan: plan},
		},
		{
			name: "request without compiled plan",
			host: prompts.HostFacts{Profiles: profiles},
			turn: prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, SurfaceID: "implement_investigate"},
		},
		{
			name: "worker request without compiled plan",
			host: prompts.HostFacts{Profiles: profiles},
			turn: prompts.TurnProjection{ProfileID: "implement", Offered: []string{"command"}},
		},
		{
			name: "unknown surface ID",
			host: prompts.HostFacts{Profiles: profiles},
			turn: prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, SurfaceID: "unknown_surface", Plan: plan},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prompts.NewReader(tc.host, tc.turn); err == nil {
				t.Fatalf("NewReader(%s) succeeded, want error", tc.name)
			}
		})
	}
}

// A reader holds the tools it is offered and their command reach; a tool it
// can only request is not held until it loads.
func TestReaderHeldComposesOfferedToolsAndReachMarks(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)

	for _, tc := range []struct {
		name        string
		profileID   string
		surfaceID   string
		offered     []string
		requestable []string
	}{
		{
			name:        "coordinator investigate with command offered",
			profileID:   prompts.CoordinatorProfileID,
			surfaceID:   "implement_investigate",
			offered:     []string{"command", "recall"},
			requestable: []string{"fetch_url"},
		},
		{
			name:        "coordinator decision adjudicate with command requestable",
			profileID:   prompts.CoordinatorProfileID,
			surfaceID:   "decision_adjudicate",
			offered:     []string{"recall"},
			requestable: []string{"command"},
		},
		{
			name:        "coordinator routing without command",
			profileID:   prompts.CoordinatorProfileID,
			surfaceID:   "implement_routing",
			offered:     []string{"recall", "task"},
			requestable: nil,
		},
		{
			name:        "worker explore_readonly with command offered",
			profileID:   "explore_readonly",
			offered:     []string{"command"},
			requestable: []string{"source_history"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := prompts.NewReader(prompts.HostFacts{
				Profiles:  profiles,
				RootCount: 1,
				WebSearch: true,
			}, prompts.TurnProjection{
				ProfileID: tc.profileID,
				SurfaceID: tc.surfaceID,
				Plan:      toolsurface.Compile(tc.offered, tc.requestable),
				Offered:   (toolsurface.Compile(tc.offered, tc.requestable)).ImmediateNames(),
			})
			testutil.FailErr(t, "NewReader", err)

			want := map[string]bool{}
			for _, name := range tc.offered {
				want[name] = true
			}
			maps.Copy(want, toolcontract.ReachMarks(want["command"], reader.Effects()))

			if got := reader.Held(); !reflect.DeepEqual(got, want) {
				t.Fatalf("Held() = %v, want %v", got, want)
			}
		})
	}
}

// A surface may list a tool an owned coordinator profile no longer allows: it
// is neither requestable nor on the floor for the reader.
func TestReaderReachFollowsTheProfileOverTheSurface(t *testing.T) {
	stock, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	profiles := slices.Clone(stock)
	for i := range profiles {
		if profiles[i].ID != prompts.CoordinatorProfileID {
			continue
		}
		profiles[i].Tools = maps.Clone(profiles[i].Tools)
		profiles[i].StickyTools = maps.Clone(profiles[i].StickyTools)
		for _, name := range []string{"task", "command"} {
			delete(profiles[i].Tools, name)
			delete(profiles[i].StickyTools, name)
		}
	}
	plan := toolsurface.Compile([]string{"recall", "command"}, []string{"task", "fetch_url"})
	for _, tc := range []struct {
		name            string
		profiles        []sandbox.ToolProfile
		offered         []string
		wantRequestable []string
		wantFloor       bool
	}{
		{name: "stock profile", profiles: stock, offered: []string{"command", "recall"}, wantRequestable: []string{"fetch_url", "task"}, wantFloor: true},
		{name: "profile without task or command", profiles: profiles, offered: []string{"recall"}, wantRequestable: []string{"fetch_url"}, wantFloor: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := prompts.NewReader(prompts.HostFacts{Profiles: tc.profiles, RootCount: 1, WebSearch: true}, prompts.TurnProjection{
				ProfileID: prompts.CoordinatorProfileID,
				SurfaceID: "implement_investigate",
				Plan:      plan,
				Offered:   tc.offered,
			})
			testutil.FailErr(t, "NewReader", err)
			if got := reader.Requestable(); !reflect.DeepEqual(got, tc.wantRequestable) {
				t.Fatalf("Requestable() = %v, want %v", got, tc.wantRequestable)
			}
			vars, err := prompts.Vars(reader, nil)
			testutil.FailErr(t, "Vars", err)
			if got := vars["command_on_floor"]; got != tc.wantFloor {
				t.Fatalf("command_on_floor = %v, want %v", got, tc.wantFloor)
			}
		})
	}
}

// can_run.<program> follows command reach: no program runs without command,
// a pure program runs under command, and a mutating program needs the
// mutation effect.
func TestReaderCanRunFollowsCommandReach(t *testing.T) {
	stock, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	plan := toolsurface.Compile([]string{"recall", "command"}, nil)
	for _, tc := range []struct {
		name    string
		offered []string
	}{
		{name: "without command", offered: []string{"recall"}},
		{name: "with command", offered: []string{"command", "recall"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := prompts.NewReader(prompts.HostFacts{Profiles: stock, RootCount: 1}, prompts.TurnProjection{
				ProfileID: prompts.CoordinatorProfileID,
				SurfaceID: "implement_investigate",
				Plan:      plan,
				Offered:   tc.offered,
			})
			testutil.FailErr(t, "NewReader", err)
			vars, err := prompts.Vars(reader, nil)
			testutil.FailErr(t, "Vars", err)
			canRun, ok := vars["can_run"].(map[string]bool)
			if !ok || len(canRun) == 0 {
				t.Fatalf("can_run = %#v, want a map over declared programs", vars["can_run"])
			}
			holdsCommand := slices.Contains(tc.offered, "command")
			if canRun["echo"] != holdsCommand {
				t.Fatalf("can_run.echo = %v, want %v", canRun["echo"], holdsCommand)
			}
			if want := holdsCommand && vars["profile_command_mutation"] == true; canRun["chmod"] != want {
				t.Fatalf("can_run.chmod = %v, want %v", canRun["chmod"], want)
			}
		})
	}
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)
	vocab, err := prompts.BuildReaderFactVocabulary(eff)
	testutil.FailErr(t, "BuildReaderFactVocabulary", err)
	if !vocab.AllowsReference("can_run.chmod") || vocab.AllowsReference("can_run.undeclared-program") {
		t.Fatal("can_run admits exactly the declared command programs")
	}
}

func TestVarsRefusesDeclaredAndReservedReaderFactsInOccurrence(t *testing.T) {
	reader, err := prompts.DeclaredReader(nil, "implement_investigate")
	testutil.FailErr(t, "DeclaredReader", err)

	if _, err := prompts.Vars(prompts.Reader{}, nil); err == nil {
		t.Fatal("Vars with zero Reader succeeded, want error")
	}

	for _, key := range []string{
		"surface_id",
		"root_count",
		"web_search_enabled",
		"caps.vision",
		"caps",
		"profile_has_read",
		"profile_custom_undeclared",
		"mints",
		"mints.read",
		"receipt_kind",
		"tool_check_custom",
		"runner_custom",
		"agent_has_skill_custom",
		"visual_show_custom",
	} {
		t.Run(key, func(t *testing.T) {
			if _, err := prompts.Vars(reader, map[string]any{key: true}); err == nil {
				t.Fatalf("Vars accepted reader fact key %q in occurrence", key)
			}
		})
	}

	merged, err := prompts.Vars(reader, map[string]any{
		"project_dir":       "/tmp/repo",
		"progress_snapshot": "done",
	})
	testutil.FailErr(t, "Vars with occurrence keys", err)
	if merged["project_dir"] != "/tmp/repo" || merged["progress_snapshot"] != "done" {
		t.Fatalf("occurrence keys missing after Vars: %v", merged)
	}
	if merged["surface_id"] != "implement_investigate" {
		t.Fatalf("reader fact surface_id = %v, want implement_investigate", merged["surface_id"])
	}
}
