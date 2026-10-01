package contract

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// toolSurfaceWireView is the serialized provider tool shape.
type toolSurfaceWireView struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func measureToolSurfaceSizes(t *testing.T) map[string]int {
	t.Helper()
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)

	sandboxCfg, err := sandbox.LoadConfig()
	contractcheck.FailErr(t, "sandbox.LoadConfig", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "sandbox.LoadToolProfiles", err)
	boundary := sandbox.NewBoundary(sandboxCfg, profiles)
	executor := tools.NewDefaultToolExecutor(tools.NewProfilePolicyEngine(boundary), reg, tools.DefaultToolProfileID)

	ctx := context.Background()
	out := make(map[string]int, len(profiles))
	for _, prof := range profiles {
		metas, err := executor.List(ctx, platform.ToolFilter{ProfileID: prof.ID})
		contractcheck.FailErr(t, "executor.List "+prof.ID, err)
		metas = tools.FilterDeferredMetas(metas, nil)
		if prof.ID == "coordinator" {
			out[prof.ID] = measureLargestCoordinatorSurface(t, metas)
			continue
		}
		views := make([]toolSurfaceWireView, 0, len(metas))
		for _, meta := range metas {
			views = append(views, toolSurfaceWireView{
				Name:        meta.Name,
				Description: meta.Description,
				Parameters:  meta.ArgsSchema,
			})
		}
		raw, err := json.Marshal(views)
		contractcheck.FailErr(t, "marshal tool surface "+prof.ID, err)
		out[prof.ID] = len(raw)
	}
	return out
}

func measureLargestCoordinatorSurface(t *testing.T, metas []tools.ToolMeta) int {
	t.Helper()
	sizes := measureCoordinatorSurfaceSizes(t, metas)
	maxBytes := 0
	for _, size := range sizes {
		if size > maxBytes {
			maxBytes = size
		}
	}
	return maxBytes
}

func measureCoordinatorSurfaceSizes(t *testing.T, metas []tools.ToolMeta) map[string]int {
	t.Helper()
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "surface.CompileToolPlans", err)
	out := make(map[string]int, len(plans))
	for surfaceID, plan := range plans {
		names := plan.ImmediateNames()
		allowed := make(map[string]bool, len(names))
		for _, name := range names {
			allowed[name] = true
		}
		views := make([]toolSurfaceWireView, 0, len(names))
		for _, meta := range metas {
			if !allowed[meta.Name] {
				continue
			}
			meta = tools.TrimCoordinatorToolMeta(meta)
			views = append(views, toolSurfaceWireView{
				Name:        meta.Name,
				Description: meta.Description,
				Parameters:  meta.ArgsSchema,
			})
		}
		raw, marshalErr := json.Marshal(views)
		contractcheck.FailErr(t, "marshal coordinator surface "+surfaceID, marshalErr)
		out[surfaceID] = len(raw)
	}
	return out
}

func TestCoordinatorInvestigateWireSurfaceStaysCompact(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)
	sandboxCfg, err := sandbox.LoadConfig()
	contractcheck.FailErr(t, "sandbox.LoadConfig", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "sandbox.LoadToolProfiles", err)
	boundary := sandbox.NewBoundary(sandboxCfg, profiles)
	executor := tools.NewDefaultToolExecutor(tools.NewProfilePolicyEngine(boundary), reg, tools.DefaultToolProfileID)
	metas, err := executor.List(context.Background(), platform.ToolFilter{ProfileID: "coordinator"})
	contractcheck.FailErr(t, "executor.List coordinator", err)
	metas = tools.FilterDeferredMetas(metas, nil)
	// Deferred families do not inflate the initial turn.
	size := measureCoordinatorSurfaceSizes(t, metas)[tools.SurfaceImplementInvestigate]
	if size == 0 || size > 32_000 {
		t.Fatalf("implement_investigate wire schema bytes = %d want 1..32000", size)
	}
}
