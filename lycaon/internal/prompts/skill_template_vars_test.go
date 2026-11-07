package prompts

import (
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSkillSurfaceTemplateVarsCoordinatorSurface(t *testing.T) {
	vars, err := SkillSurfaceTemplateVars(SkillSurfaceVarsInput{
		ProfileID:        CoordinatorProfileID,
		SurfaceID:        "implement_routing",
		Offered:          []string{"command", "task", "ask_user"},
		RootCount:        2,
		DelegationBudget: spawn.DefaultWorkerToolBudget(),
	})
	testutil.FailErr(t, "coordinator vars", err)
	if vars["is_coordinator"] != true {
		t.Fatalf("is_coordinator = %v", vars["is_coordinator"])
	}
	if vars["root_count"] != 2 {
		t.Fatalf("root_count = %v", vars["root_count"])
	}
	offered, _ := vars["surface_offered"].([]string)
	if !sort.StringsAreSorted(offered) || len(offered) == 0 {
		t.Fatalf("surface_offered = %v", offered)
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

func TestSkillSurfaceTemplateVarsWorkerProfile(t *testing.T) {
	vars, err := SkillSurfaceTemplateVars(SkillSurfaceVarsInput{
		ProfileID:        "worker_readonly",
		Offered:          []string{"command", "summarize", "recall"},
		OnWorkerBranch:   true,
		HasGit:           true,
		RootCount:        1,
		DelegationBudget: spawn.DefaultWorkerToolBudget(),
	})
	testutil.FailErr(t, "worker vars", err)
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
	if _, ok := vars["surface_offered"]; ok {
		t.Fatal("worker vars must not carry coordinator surface rosters")
	}
}

func TestSkillSurfaceTemplateVarsCallerShapeSurvivesMerges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input SkillSurfaceVarsInput
		shape map[string]any
	}{
		{
			name:  "coordinator",
			input: SkillSurfaceVarsInput{ProfileID: CoordinatorProfileID, SurfaceID: "implement_routing", OnWorkerBranch: false, HasGit: true, RootCount: 3},
			shape: map[string]any{"is_coordinator": true, "on_worker_branch": false, "has_git": true, "root_count": 3},
		},
		{
			name:  "worker",
			input: SkillSurfaceVarsInput{ProfileID: "worker_readonly", OnWorkerBranch: true, HasGit: false, RootCount: 1},
			shape: map[string]any{"is_coordinator": false, "on_worker_branch": true, "has_git": false, "root_count": 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars, err := SkillSurfaceTemplateVars(tc.input)
			testutil.FailErr(t, "vars", err)
			for k, want := range tc.shape {
				if vars[k] != want {
					t.Fatalf("%s = %v, want %v", k, vars[k], want)
				}
			}
		})
	}
}
