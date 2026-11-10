package prompts

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestSkillRuntimePlanBoundsCatalogCapabilities(t *testing.T) {
	for _, profile := range []string{CoordinatorProfileID, "web_research", "implement"} {
		for _, state := range []struct {
			name          string
			plan          toolsurface.Plan
			search, fetch bool
		}{
			{"closed", toolsurface.Compile(nil, nil), false, false},
			{"research disabled", toolsurface.Compile([]string{"recall"}, nil), false, false},
			{"search only", toolsurface.Compile([]string{"web_search"}, nil), true, false},
			{"family", toolsurface.Compile([]string{"web_search", "fetch_url"}, nil), true, true},
		} {
			t.Run(profile+"/"+state.name, func(t *testing.T) {
				in := SkillSurfaceVarsInput{ProfileID: profile, Plan: state.plan, Offered: state.plan.ImmediateNames(), RootCount: 1}
				if profile == CoordinatorProfileID {
					in.SurfaceID = "implement_investigate"
				}
				vars, err := SkillSurfaceTemplateVars(in)
				testutil.FailErr(t, "render runtime capability facts", err)
				for name, want := range map[string]bool{"web_search": state.search, "fetch_url": state.fetch, "command": false, "task": false} {
					if vars["profile_has_"+name] != want {
						t.Fatalf("%s=%v want=%v", name, vars["profile_has_"+name], want)
					}
					if requestable, _ := vars["profile_can_request"].(map[string]bool); !state.plan.Addressable(name) && requestable[name] {
						t.Fatalf("absent %s advertised as requestable", name)
					}
				}
				if offered, ok := vars["surface_offered"].([]string); ok {
					for _, name := range offered {
						if !state.plan.Addressable(name) {
							t.Fatalf("catalog reintroduced %s", name)
						}
					}
				}
			})
		}
	}
}

func TestCoordinatorSkillCanTeachDeferredToolWithinRuntimePlan(t *testing.T) {
	plan := toolsurface.Compile([]string{"request_tools"}, []string{"fetch_url", "web_search"})
	vars, err := SkillSurfaceTemplateVars(SkillSurfaceVarsInput{ProfileID: CoordinatorProfileID, SurfaceID: "implement_investigate", Offered: plan.ImmediateNames(), Plan: plan})
	testutil.FailErr(t, "deferred research vars", err)
	if vars["profile_has_fetch_url"] != true || vars["profile_has_web_search"] != true {
		t.Fatal("reachable research workflow was blanked")
	}
	if offered := vars["surface_offered"].([]string); slices.Contains(offered, "command") {
		t.Fatal("runtime-excluded command was taught")
	}
}

func TestPromptSurfaceRespectsClosedRuntimePlan(t *testing.T) {
	vars := map[string]any{}
	testutil.FailErr(t, "closed runtime surface", MergeCoordinatorSurfacePathVars("implement_investigate", nil, vars, SurfaceTurn{Plan: toolsurface.Compile(nil, nil), Loaded: map[string]bool{"web_search": true, "fetch_url": true, "command": true}}))
	if len(vars["surface_offered"].([]string)) != 0 || vars["more_tools_loadable"] != false {
		t.Fatalf("closed plan widened: %v", vars)
	}
}
