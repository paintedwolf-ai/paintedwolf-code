package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const (
	surfaceImplementRouting        = "implement_routing"
	surfaceImplementDispatch       = "implement_dispatch"
	surfaceImplementSynthesis      = "implement_synthesis"
	surfaceImplementOverlayPromote = "implement_overlay_promote"
	surfaceImplementPark           = "implement_park"
)

// orchestrationLifecycleTools remain callable on pinned implement surfaces.
var orchestrationLifecycleTools = []string{
	"answer_decision",
	"decline_worker_budget",
	"extend_worker_budget",
	"update_progress",
	"worker_cancel",
	"wait",
}

// orchestrationPinnedSurfaces remain active while parallel work is open.
var orchestrationPinnedSurfaces = []string{
	surfaceImplementOverlayPromote,
	surfaceImplementPark,
}

// specialistSpawnAgents are task targets, not wire tools.
var specialistSpawnAgents = []string{
	"security-reviewer",
	"implementer",
}

func loadImplementSurfaces(t *testing.T) map[string][]string {
	t.Helper()
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "CompileToolPlans", err)
	surfaces := make(map[string][]string, len(plans))
	for id, plan := range plans {
		surfaces[id] = plan.ImmediateNames()
	}
	return surfaces
}

// fileText reads an embedded or linked pack file.
func fileText(t *testing.T, at extpacks.Source) string {
	t.Helper()
	data, err := at.Read()
	contractcheck.FailErr(t, "read "+at.String(), err)
	return string(data)
}

func coordinatorProfileGrantsTool(profile sandbox.ToolProfile, tool string) bool {
	return profile.Tools[tool]
}

func toolOnImplementSurface(surfaceTools []string, tool string) bool {
	return contractcheck.ContainsString(surfaceTools, tool)
}
