package promptloop

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResearchFamilyReachesCoordinatorAndWorkerCalls(t *testing.T) {
	for _, predicted := range []bool{false, true} {
		t.Run(map[bool]string{false: "requested", true: "predicted"}[predicted], func(t *testing.T) {
			ledger := turnload.NewLedger()
			ledger.BeginTurn("s", turnload.TurnOpening{Loadable: []string{"web_search", "fetch_url"}}, turnload.Decision{})
			reg := tools.NewDefaultRegistry()
			for _, name := range []string{"web_search", "fetch_url"} {
				testutil.FailErr(t, "register research tool", reg.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }))
			}
			plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_investigate"}, 0)
			testutil.FailErr(t, "compile rootless surface", err)
			if predicted {
				ledger.BeginTurn("s", turnload.TurnOpening{Loadable: plan.DeferredNames()}, turnload.Decision{Tools: map[string]float64{"web_search": .9}})
			} else {
				testutil.FailErr(t, "register loader", tools.RegisterRequestTools(reg, tools.RequestToolsDeps{Activation: ledger, Boundary: recoveryCatalogBoundary{}}))
				raw, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "web search"}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "s"}, Turn: tools.InvocationTurn{TurnSurfaceID: "implement_investigate", TurnToolPlan: plan}})
				testutil.FailErr(t, "load research", err)
				var result turnload.RequestToolsResult
				testutil.FailErr(t, "decode load result", json.Unmarshal([]byte(raw), &result))
				if !slices.Equal(result.Loaded, []string{"fetch_url", "web_search"}) {
					t.Fatalf("loaded = %+v", result)
				}
			}
			for _, enabled := range []bool{false, true} {
				loop := NewPromptLoop(PromptLoopDeps{Context: ContextDeps{WebSearchEnabled: func() bool { return enabled }}})
				metas := []tools.ToolMeta{{Name: "web_search", Deferred: true}, {Name: "fetch_url", Deferred: true}}
				sess := &api.Session{ID: "s"}
				coordinator := loop.Context.compileRuntimeToolPlan(plan, api.CoordinatorRunContext{}, metas, ledger.Active("s"), tools.MCPToolPlan{}, true, sess)
				worker := loop.Context.compileWorkerToolPlan(t.Context(), sess, metas, ledger.Active("s"))
				for _, current := range []toolsurface.Plan{coordinator, worker} {
					for _, name := range []string{"web_search", "fetch_url"} {
						if current.Immediate(name) != enabled || (!enabled && current.Addressable(name)) {
							t.Fatalf("enabled=%v, %s availability=%v", enabled, name, current.Availability(name))
						}
					}
				}
			}
		})
	}
}
