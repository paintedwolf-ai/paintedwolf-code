package prompts

import (
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// skillTestReader offers a surface's addressable tools, or a worker
// profile's offered names, with web search on.
func skillTestReader(t *testing.T, host HostFacts, turn TurnProjection) Reader {
	t.Helper()
	if len(host.Profiles) == 0 {
		profiles, err := sandbox.LoadToolProfiles()
		testutil.FailErr(t, "load tool profiles", err)
		host.Profiles = profiles
	}
	if turn.SurfaceID != "" {
		plan, err := CompileSurfacePlan(turn.SurfaceID, max(host.RootCount, 1))
		testutil.FailErr(t, "compile surface plan", err)
		turn.Plan = plan.Promote(plan.AddressableNames()...)
		turn.Offered = turn.Plan.ImmediateNames()
	} else {
		turn.Declared = true
	}
	host.WebSearch = true
	reader, err := NewReader(host, turn)
	testutil.FailErr(t, "new reader", err)
	return reader
}

func TestSkillVarsCoordinatorSurface(t *testing.T) {
	reader := skillTestReader(t, HostFacts{RootCount: 2}, TurnProjection{ProfileID: CoordinatorProfileID, SurfaceID: "implement_routing"})
	vars, err := SkillVars(reader)
	testutil.FailErr(t, "skill vars", err)
	if vars["is_coordinator"] != true {
		t.Fatalf("is_coordinator = %v", vars["is_coordinator"])
	}
	if vars["root_count"] != 2 {
		t.Fatalf("root_count = %v", vars["root_count"])
	}
	offered := reader.Offered()
	if !sort.StringsAreSorted(offered) || len(offered) == 0 {
		t.Fatalf("reader.Offered() = %v", offered)
	}
	if vars["profile_has_command"] != true {
		t.Fatalf("profile_has_command = %v", vars["profile_has_command"])
	}
	if vars["profile_has_task"] != true {
		t.Fatalf("coordinator floor must offer task: %v", vars["profile_has_task"])
	}
	if _, ok := vars["on_worker_branch"]; !ok {
		t.Fatal("on_worker_branch must be set")
	}
	if _, ok := vars["has_git"]; !ok {
		t.Fatal("has_git must be set")
	}
}

func TestSkillVarsWorkerProfile(t *testing.T) {
	offered := []string{"command", "summarize", "recall"}
	reader := skillTestReader(t, HostFacts{OnWorkerBranch: true, HasGit: true, RootCount: 1}, TurnProjection{ProfileID: "worker_readonly", Offered: offered})
	vars, err := SkillVars(reader)
	testutil.FailErr(t, "skill vars", err)
	if vars["is_coordinator"] != false {
		t.Fatalf("is_coordinator = %v", vars["is_coordinator"])
	}
	if vars["on_worker_branch"] != true {
		t.Fatalf("on_worker_branch = %v", vars["on_worker_branch"])
	}
	if vars["has_git"] != true {
		t.Fatalf("has_git = %v", vars["has_git"])
	}
	if vars["root_count"] != 1 {
		t.Fatalf("root_count = %v", vars["root_count"])
	}
	if vars["profile_has_command"] != true {
		t.Fatalf("profile_has_command = %v", vars["profile_has_command"])
	}
	if vars["profile_has_task"] != false {
		t.Fatalf("worker must not hold task: %v", vars["profile_has_task"])
	}
	if vars["surface_id"] != "" {
		t.Fatalf("worker vars must not carry coordinator surface_id: %v", vars["surface_id"])
	}
}

func TestSkillVarsCallerShapeSurvivesMerges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		host  HostFacts
		turn  TurnProjection
		shape map[string]any
	}{
		{
			name:  "coordinator",
			host:  HostFacts{OnWorkerBranch: false, HasGit: true, RootCount: 3},
			turn:  TurnProjection{ProfileID: CoordinatorProfileID, SurfaceID: "implement_routing"},
			shape: map[string]any{"is_coordinator": true, "on_worker_branch": false, "has_git": true, "root_count": 3},
		},
		{
			name:  "worker",
			host:  HostFacts{OnWorkerBranch: true, HasGit: false, RootCount: 1},
			turn:  TurnProjection{ProfileID: "worker_readonly"},
			shape: map[string]any{"is_coordinator": false, "on_worker_branch": true, "has_git": false, "root_count": 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars, err := SkillVars(skillTestReader(t, tc.host, tc.turn))
			testutil.FailErr(t, "skill vars", err)
			for k, want := range tc.shape {
				if vars[k] != want {
					t.Fatalf("%s = %v, want %v", k, vars[k], want)
				}
			}
		})
	}
}

func TestSkillVarsWorkerOfferedMarksDeferredLoaded(t *testing.T) {
	offered := []string{"command", "held_result"}
	reader := skillTestReader(t, HostFacts{HasGit: true, RootCount: 1}, TurnProjection{ProfileID: "worker_readonly", Offered: offered})
	vars, err := SkillVars(reader)
	testutil.FailErr(t, "skill vars", err)
	if vars["profile_has_held_result"] != true {
		t.Fatalf("offered deferred tool must render as held: %v", vars["profile_has_held_result"])
	}
	if vars["profile_has_held_stop"] != false {
		t.Fatalf("deferred tool outside Offered must stay requestable: %v", vars["profile_has_held_stop"])
	}
	if vars["profile_has_command"] != true {
		t.Fatalf("sticky tool must render as held: %v", vars["profile_has_command"])
	}
}

func TestSkillVarsCoordinatorAddressableTools(t *testing.T) {
	reader := skillTestReader(t, HostFacts{RootCount: 1}, TurnProjection{ProfileID: CoordinatorProfileID, SurfaceID: "implement_routing"})
	vars, err := SkillVars(reader)
	testutil.FailErr(t, "skill vars", err)
	if vars["profile_has_scan_query"] != true {
		t.Fatalf("floor tool outside Offered must render as addressable: %v", vars["profile_has_scan_query"])
	}
	if vars["profile_has_command_output"] != true {
		t.Fatalf("loadable tool must render as addressable before request_tools: %v", vars["profile_has_command_output"])
	}
	if vars["profile_has_task"] != true {
		t.Fatalf("offered tool must render as held: %v", vars["profile_has_task"])
	}
}
