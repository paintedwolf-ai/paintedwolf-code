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

func TestReaderHeldComposesToolsAndReachMarks(t *testing.T) {
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
			for _, name := range tc.requestable {
				want[name] = true
			}
			maps.Copy(want, toolcontract.ReachMarks(want["command"], reader.Effects()))

			if got := reader.Held(); !reflect.DeepEqual(got, want) {
				t.Fatalf("Held() = %v, want %v", got, want)
			}
		})
	}
}
