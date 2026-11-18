package prompts_test

import (
	"maps"
	"reflect"
	"testing"

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
		in   prompts.ReaderInput
	}{
		{
			name: "nil profiles",
			in:   prompts.ReaderInput{ProfileID: "implement", Plan: plan},
		},
		{
			name: "empty profile ID",
			in:   prompts.ReaderInput{Profiles: profiles, Plan: plan},
		},
		{
			name: "unknown profile ID",
			in:   prompts.ReaderInput{ProfileID: "unknown_profile", Profiles: profiles, Plan: plan},
		},
		{
			name: "surface without compiled plan",
			in:   prompts.ReaderInput{ProfileID: prompts.CoordinatorProfileID, SurfaceID: "implement_investigate", Profiles: profiles},
		},
		{
			name: "unknown surface ID",
			in:   prompts.ReaderInput{ProfileID: prompts.CoordinatorProfileID, SurfaceID: "unknown_surface", Plan: plan, Profiles: profiles},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prompts.NewReader(tc.in); err == nil {
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
			reader, err := prompts.NewReader(prompts.ReaderInput{
				ProfileID: tc.profileID,
				SurfaceID: tc.surfaceID,
				Plan:      toolsurface.Compile(tc.offered, tc.requestable),
				Profiles:  profiles,
				RootCount: 1,
				WebSearch: true,
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
