package prompts

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestSkillRuntimePlanBoundsCatalogCapabilities(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
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
				in := ReaderInput{
					ProfileID: profile,
					Profiles:  profiles,
					Plan:      state.plan,
					Visible:   state.plan.ImmediateNames(),
					Loaded:    toolNameSet(state.plan.AddressableNames()),
					RootCount: 1,
					WebSearch: true,
				}
				if profile == CoordinatorProfileID {
					in.SurfaceID = "implement_investigate"
					in.Visible = nil
				}
				reader, err := NewReader(in)
				testutil.FailErr(t, "new reader", err)
				vars := SkillVars(reader)
				for name, want := range map[string]bool{"web_search": state.search, "fetch_url": state.fetch, "command": false, "task": false} {
					if vars["profile_has_"+name] != want {
						t.Fatalf("%s=%v want=%v", name, vars["profile_has_"+name], want)
					}
					if requestable, _ := vars["profile_can_request"].(map[string]bool); !state.plan.Addressable(name) && requestable[name] {
						t.Fatalf("absent %s advertised as requestable", name)
					}
				}
				for _, name := range reader.Offered() {
					if !state.plan.Addressable(name) {
						t.Fatalf("catalog reintroduced %s", name)
					}
				}
			})
		}
	}
}

func TestCoordinatorSkillCanTeachDeferredToolWithinRuntimePlan(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	plan := toolsurface.Compile([]string{"request_tools"}, []string{"fetch_url", "web_search"})
	reader, err := NewReader(ReaderInput{
		ProfileID: CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  profiles,
		Plan:      plan,
		Loaded:    toolNameSet(plan.AddressableNames()),
		WebSearch: true,
	})
	testutil.FailErr(t, "new reader", err)
	vars := SkillVars(reader)
	if vars["profile_has_fetch_url"] != true || vars["profile_has_web_search"] != true {
		t.Fatal("reachable research workflow was blanked")
	}
	if slices.Contains(reader.Offered(), "command") {
		t.Fatal("runtime-excluded command was taught")
	}
}

func TestPromptSurfaceRespectsClosedRuntimePlan(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := NewReader(ReaderInput{
		ProfileID: CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  profiles,
		Plan:      toolsurface.Compile(nil, nil),
		Loaded:    map[string]bool{"web_search": true, "fetch_url": true, "command": true},
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "closed runtime surface", err)
	vars := reader.Facts()
	if len(reader.Offered()) != 0 || vars["more_tools_loadable"] != false {
		t.Fatalf("closed plan widened: %v", vars)
	}
}
