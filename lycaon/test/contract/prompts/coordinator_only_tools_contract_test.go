package contract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// coordinatorOnlyTools are the tools whose handlers refuse every agent but the
// coordinator. TestCoordinatorOnlyGuardInventory holds them to the profiles'
// grants: only the coordinator profile may offer them.
var coordinatorOnlyTools = []string{
	"workflow_advance",
	"workflow_transition",
	"fanout_plan",
	"submit_verdict",
	"workflow_catalog_summaries",
	"workflow_compose",
	"workflow_compose_from_template",
	"workflow_persist",
	"workflow_user_feedback",
	"ask_user",
}

// TestCoordinatorOnlyToolsRejectNonCoordinatorAgents drives every tool that
// claims a coordinator-only role with Agent="implementer" and asserts each
// rejects with a coordinator-role error. Adding a new coordinator-only tool
// without enforcing the guard will land here as a missing rejection.
func TestCoordinatorOnlyToolsRejectNonCoordinatorAgents(t *testing.T) {
	reg := toolfixture.RegisterCatalogToolsForContract(t)

	// Some tools validate arguments before the role guard, so they get args
	// that pass validation.
	knownArgs := map[string]map[string]any{
		"workflow_compose":               {"manifest": map[string]any{}},
		"workflow_compose_from_template": {"template_id": "x"},
		// The rest take no arguments.
	}

	for _, tool := range coordinatorOnlyTools {
		t.Run(tool, func(t *testing.T) {
			args := map[string]any{}
			if a, ok := knownArgs[tool]; ok {
				args = a
			}
			_, err := reg.Run(context.Background(), tool, args, tools.ToolContext{
				Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
					ActiveRootID: "r1"},
				Identity: tools.InvocationIdentity{Agent: "implementer",
					SessionID: "sess-1"},
			})
			if err == nil {
				t.Fatalf("tool %q accepted non-coordinator agent — guard missing", tool)
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "coordinator") {
				t.Fatalf("tool %q rejected non-coordinator but error %q does not mention 'coordinator'", tool, err.Error())
			}
		})
	}
}

// TestCoordinatorOnlyGuardInventory holds the guarded tools to the declared
// role: the coordinator profile grants each one and every other profile is
// refused it by the sandbox boundary.
func TestCoordinatorOnlyGuardInventory(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	boundary := loadBundledSandboxBoundary(t)
	others := 0
	for _, prof := range profiles {
		if prof.ID != orchestration.ProfileCoordinator {
			others++
		}
	}
	if others == 0 {
		t.Fatal("no non-coordinator tool profiles loaded")
	}
	for _, tool := range coordinatorOnlyTools {
		contractcheck.FailErr(t, "coordinator grant of "+tool,
			boundary.AssertToolAllowed(t.Context(), orchestration.ProfileCoordinator, tool, sandbox.ToolAccessProfile))
		for _, prof := range profiles {
			if prof.ID == orchestration.ProfileCoordinator {
				continue
			}
			if err := boundary.AssertToolAllowed(t.Context(), prof.ID, tool, sandbox.ToolAccessProfile); !errors.Is(err, sandbox.ErrToolNotAllowedByProfile) {
				t.Errorf("profile %s and coordinator-only tool %s: err = %v, want ErrToolNotAllowedByProfile", prof.ID, tool, err)
			}
		}
	}
}
