package prompts_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResearchPromptInventoryFollowsSettingAndFamilyFloor(t *testing.T) {
	catalog, err := surfacecatalog.Load()
	testutil.FailErr(t, "load surfaces", err)
	for _, id := range catalog.SurfaceIDs() {
		row, err := catalog.Surface(id)
		testutil.FailErr(t, "surface", err)
		floor, err := prompts.LoadCoordinatorSurfaceFloor(id)
		testutil.FailErr(t, "prompt floor", err)
		for _, name := range []string{"web_search", "fetch_url"} {
			if slices.Contains(floor, name) != slices.Contains(row.FloorTools(), name) {
				t.Fatalf("%s prompt floor differs from runtime: %v", id, floor)
			}
		}
	}
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load profiles", err)
	for _, surface := range []string{"implement_investigate", "observe_plan"} {
		plan, err := prompts.CompileSurfacePlan(surface, 1)
		testutil.FailErr(t, "compile plan", err)
		for _, loaded := range []map[string]bool{nil, {"web_search": true, "fetch_url": true}} {
			in := prompts.ReaderInput{
				ProfileID: prompts.CoordinatorProfileID,
				SurfaceID: surface,
				Profiles:  profiles,
				Plan:      plan,
				Loaded:    loaded,
				RootCount: 1,
				HasGit:    true,
				WebSearch: false,
			}
			reader, err := prompts.NewReader(in)
			testutil.FailErr(t, "disabled research surface", err)
			vars := reader.Facts()
			for _, name := range []string{"web_search", "fetch_url"} {
				if vars["profile_has_"+name] != false || slices.Contains(reader.Offered(), name) {
					t.Fatalf("disabled tool %s taught on %s: %v", name, surface, vars)
				}
				if requestable, _ := vars["profile_can_request"].(map[string]bool); requestable[name] {
					t.Fatalf("disabled tool %s requestable", name)
				}
				if slices.Contains(reader.Requestable(), name) {
					t.Fatalf("disabled tool %s in discovery", name)
				}
			}
		}
	}
}
