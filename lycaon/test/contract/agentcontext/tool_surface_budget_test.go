package contract

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
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

// toolSchemas is one surface's tool schemas: the upfront set every request
// carries, and the requestable tools with each one's wire size.
type toolSchemas struct {
	Upfront     int
	upfront     map[string]bool
	requestable map[string]int
}

// Largest returns the n largest requestable tools, largest first: what one
// request_tools load can add at most.
func (s toolSchemas) Largest(n int) []string {
	names := make([]string, 0, len(s.requestable))
	for name := range s.requestable {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(s.requestable[b]-s.requestable[a], strings.Compare(a, b))
	})
	return names[:min(n, len(names))]
}

// Loads returns the wire size the named requestable tools add.
func (s toolSchemas) Loads(names []string) int {
	total := 0
	for _, name := range names {
		total += s.requestable[name]
	}
	return total
}

// AllLoaded is the surface's size once every requestable tool has loaded.
func (s toolSchemas) AllLoaded() int {
	total := s.Upfront
	for _, size := range s.requestable {
		total += size
	}
	return total
}

// toolSurfaceMeasurements holds the tool schemas of each coordinator surface
// and each worker tool profile: the two classes the budget limits apart.
type toolSurfaceMeasurements struct {
	Coordinator map[string]toolSchemas
	Workers     map[string]toolSchemas
}

func measureToolSurfaces(t *testing.T) toolSurfaceMeasurements {
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
	out := toolSurfaceMeasurements{Workers: map[string]toolSchemas{}}
	for _, prof := range profiles {
		metas, err := executor.List(ctx, platform.ToolFilter{ProfileID: prof.ID})
		contractcheck.FailErr(t, "executor.List "+prof.ID, err)
		if prof.ID == "coordinator" {
			out.Coordinator = measureCoordinatorSurfaces(t, metas)
			continue
		}
		var sticky, requestable []string
		for _, meta := range metas {
			if prof.DeferredTools[meta.Name] {
				requestable = append(requestable, meta.Name)
			} else {
				sticky = append(sticky, meta.Name)
			}
		}
		out.Workers[prof.ID] = schemasFor(t, prof.ID, metas, sticky, requestable, false)
	}
	return out
}

// measureCoordinatorSurfaces sizes each coordinator surface the way the
// prompt loop offers it: the plan's immediate tools upfront, its deferred
// tools on request, all trimmed for the coordinator.
func measureCoordinatorSurfaces(t *testing.T, metas []tools.ToolMeta) map[string]toolSchemas {
	t.Helper()
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "surface.CompileToolPlans", err)
	out := make(map[string]toolSchemas, len(plans))
	for surfaceID, plan := range plans {
		out[surfaceID] = schemasFor(t, surfaceID, metas, plan.ImmediateNames(), plan.DeferredNames(), true)
	}
	return out
}

func schemasFor(t *testing.T, label string, metas []tools.ToolMeta, upfront, requestable []string, coordinator bool) toolSchemas {
	t.Helper()
	byName := make(map[string]tools.ToolMeta, len(metas))
	for _, meta := range metas {
		byName[meta.Name] = meta
	}
	pick := func(names []string) []tools.ToolMeta {
		var out []tools.ToolMeta
		for _, name := range names {
			if meta, ok := byName[name]; ok {
				out = append(out, meta)
			}
		}
		return out
	}
	schemas := toolSchemas{
		Upfront:     marshalToolSurface(t, label, pick(upfront), coordinator),
		upfront:     map[string]bool{},
		requestable: map[string]int{},
	}
	for _, name := range upfront {
		schemas.upfront[name] = true
	}
	for _, meta := range pick(requestable) {
		// One schema in a request's tool list, with its separating comma.
		schemas.requestable[meta.Name] = marshalToolSurface(t, label, []tools.ToolMeta{meta}, coordinator) - 1
	}
	return schemas
}

func marshalToolSurface(t *testing.T, label string, metas []tools.ToolMeta, coordinator bool) int {
	t.Helper()
	views := make([]toolSurfaceWireView, 0, len(metas))
	for _, meta := range metas {
		if coordinator {
			meta = tools.TrimCoordinatorToolMeta(meta)
		}
		views = append(views, toolSurfaceWireView{Name: meta.Name, Description: meta.Description, Parameters: meta.ArgsSchema})
	}
	raw, err := json.Marshal(views)
	contractcheck.FailErr(t, "marshal tool surface "+label, err)
	return len(raw)
}

func TestCoordinatorInvestigateWireSurfaceStaysCompact(t *testing.T) {
	size := measureToolSurfaces(t).Coordinator[tools.SurfaceImplementInvestigate].Upfront
	// Deferred families do not inflate the initial turn.
	if size == 0 || size > 32_000 {
		t.Fatalf("implement_investigate wire schema bytes = %d want 1..32000", size)
	}
}
