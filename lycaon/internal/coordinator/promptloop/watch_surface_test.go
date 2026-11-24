package promptloop

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

func buildWatchSurfaceMetadata(t *testing.T) *toolexecution.Metadata {
	t.Helper()
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: filepath.Join("..", "..", "..")})
	testutil.FailErr(t, "toolhost.NewRuntime", err)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "toolschema.LoadSchemaDir", err)
	for name := range schemas.Tools {
		if _, exists := rt.Registry.Meta(name); !exists {
			_ = rt.Registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				return "{}", nil
			})
		}
	}
	_ = watch.RegisterWatchTools(rt.Registry, watch.ToolDeps{})
	rt.Executor.Metadata.SetToolSchemas(schemas)
	return rt.Executor.Metadata
}

func TestCoordinatorSurfacesWatchPlacementAndPromotionMatrix(t *testing.T) {
	metaSvc := buildWatchSurfaceMetadata(t)
	cat, err := surfacecatalog.Load()
	testutil.FailErr(t, "surfacecatalog.Load", err)

	floorSurfaces := map[string]bool{
		"implement_investigate": true,
		"observe_investigate":   true,
		"implement_routing":     true,
		"plan_research":         true,
		"plan_review":           true,
		"survey_execute":        true,
		"review_adjudicate":     true,
		"decision_adjudicate":   true,
		"plan_execute":          true,
	}

	var activeWatches int
	projectBelongs := true
	rootCount := 1

	policyEngine := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		ToolLister:       metaSvc,
		ProjectRootCount: func(context.Context, *api.Session) int { return rootCount },
		ProjectChat:      func(context.Context, *api.Session) bool { return projectBelongs },
	})

	loop := NewPromptLoop(PromptLoopDeps{
		Context: ContextDeps{
			Policy:           policyEngine,
			ProjectRootCount: func(context.Context, *api.Session) int { return rootCount },
			LiveResources: func(string) toolcontract.ResourcePresence {
				return toolcontract.ResourcePresence{Watches: activeWatches > 0}
			},
		},
	})

	const defaultLimit = 40000
	const promoteExceptionLimit = 44000

	for _, surfaceID := range cat.SurfaceIDs() {
		for _, watchesCount := range []int{0, 1, 8} {
			activeWatches = watchesCount
			rootCount = 1
			projectBelongs = true

			sess := &api.Session{ID: "sess-1", ProjectID: "proj-1", Posture: api.SessionPostureBuild}
			basePlan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, rootCount)
			testutil.FailErr(t, "CompileToolPlan "+surfaceID, err)
			waitAddressableOnBase := basePlan.Addressable("wait")

			listCtx := toolpolicy.WithCoordinatorSurface(t.Context(), surfaceID)
			all := policyEngine.ListForPrompt(listCtx, sess, prompts.CoordinatorProfileID)
			compiled := loop.Context.compileRuntimeToolPlanForSurface(basePlan, surfaceID, api.CoordinatorRunContext{}, all, nil, tools.MCPToolPlan{}, basePlan.Immediate("request_tools"), sess)
			compiled = filterToolPlanForRootCount(compiled, rootCount)
			offered := trimCoordinatorToolMetasForPlan(compiled, all, nil, nil)

			hasTool := func(name string) bool {
				return slices.ContainsFunc(offered, func(m tools.ToolMeta) bool { return m.Name == name })
			}

			wantFloorWatch := floorSurfaces[surfaceID]
			if hasTool("watch") != wantFloorWatch {
				t.Fatalf("surface %s (watches=%d): watch offered=%v, want %v", surfaceID, watchesCount, hasTool("watch"), wantFloorWatch)
			}

			wantControl := watchesCount > 0 && surface.AllowsWatchManagement(surfaceID)
			if hasTool("watch_control") != wantControl {
				t.Fatalf("surface %s (watches=%d): watch_control offered=%v, want %v", surfaceID, watchesCount, hasTool("watch_control"), wantControl)
			}

			if watchesCount > 0 && surface.AllowsWatchManagement(surfaceID) && waitAddressableOnBase && !hasTool("wait") {
				t.Fatalf("surface %s (watches=%d): wait was addressable on base plan and must promote", surfaceID, watchesCount)
			}
			if watchesCount > 0 && !waitAddressableOnBase && hasTool("wait") {
				t.Fatalf("surface %s (watches=%d): wait promoted despite not being addressable on surface", surfaceID, watchesCount)
			}

			serialized, err := json.Marshal(offered)
			testutil.FailErr(t, "marshal offered tools", err)
			limit := defaultLimit
			if surfaceID == surface.SurfaceImplementOverlayPromote {
				limit = promoteExceptionLimit
			}
			if len(serialized) > limit {
				t.Fatalf("surface %s (watches=%d) serialized tool bytes = %d > budget %d", surfaceID, watchesCount, len(serialized), limit)
			}
		}
	}

	activeWatches = 1
	rootCount = 0
	projectBelongs = false
	sess := &api.Session{ID: "sess-draft", ProjectID: "draft-proj", Posture: api.SessionPostureBuild}
	basePlan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_investigate"}, 0)
	testutil.FailErr(t, "CompileToolPlan rootless", err)
	all := policyEngine.ListForPrompt(toolpolicy.WithCoordinatorSurface(t.Context(), "implement_investigate"), sess, prompts.CoordinatorProfileID)
	compiled := filterToolPlanForRootCount(loop.Context.compileRuntimeToolPlanForSurface(basePlan, "implement_investigate", api.CoordinatorRunContext{}, all, nil, tools.MCPToolPlan{}, true, sess), 0)
	offered := trimCoordinatorToolMetasForPlan(compiled, all, nil, nil)

	var watchMeta *tools.ToolMeta
	hasControl := false
	for i := range offered {
		if offered[i].Name == "watch" {
			watchMeta = &offered[i]
		}
		if offered[i].Name == "watch_control" {
			hasControl = true
		}
	}
	if watchMeta == nil || !hasControl {
		t.Fatalf("rootless implement_investigate with active watch must offer watch (%v) and watch_control (%v)", watchMeta != nil, hasControl)
	}
	props, _ := watchMeta.ArgsSchema["properties"].(map[string]any)
	if _, hasStanding := props["standing"]; hasStanding {
		t.Fatalf("draft chat without project must omit standing from watch schema")
	}
	conds, _ := props["conditions"].(map[string]any)
	items, _ := conds["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)
	kindEnum, _ := itemProps["kind"].(map[string]any)["enum"].([]any)
	for _, v := range kindEnum {
		if v == "file_change" || v == "git_ref" {
			t.Fatalf("rootless watch schema must omit %v, got %v", v, kindEnum)
		}
	}
}

func TestWorkerProfilesWatchPruningAndIneligibleProfile(t *testing.T) {
	metaSvc := buildWatchSurfaceMetadata(t)

	var activeWatches bool
	policyEngine := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		ToolLister:       metaSvc,
		ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
		ProjectChat:      func(context.Context, *api.Session) bool { return true },
	})
	loop := NewPromptLoop(PromptLoopDeps{
		Context: ContextDeps{
			Policy: policyEngine,
			LiveResources: func(string) toolcontract.ResourcePresence {
				return toolcontract.ResourcePresence{Watches: activeWatches}
			},
		},
	})

	workerSess := &api.Session{ID: "worker-sess", ParentSessionID: "root-sess", ProjectID: "proj-1"}

	for _, profileID := range []string{"implement", "explore_readonly", "web_research"} {
		activeWatches = false
		idleMetas, _, _, err := loop.Context.coordinatorToolsForTurn(t.Context(), workerSess, profileID, nil, 0, 10, inject.CoordinatorTurnFrame{}, nil)
		testutil.FailErr(t, "worker tools idle "+profileID, err)

		var watchSchema map[string]any
		for _, m := range idleMetas {
			if m.Name == "watch" {
				watchSchema = m.ArgsSchema
			}
			if m.Name == "watch_control" {
				t.Fatalf("worker %s idle turn should defer watch_control", profileID)
			}
		}
		if watchSchema == nil {
			t.Fatalf("eligible worker %s missing sticky watch", profileID)
		}
		props, _ := watchSchema["properties"].(map[string]any)
		if _, hasStanding := props["standing"]; hasStanding {
			t.Fatalf("worker %s must omit standing field", profileID)
		}
		delivEnum, _ := props["delivery"].(map[string]any)["enum"].([]any)
		if len(delivEnum) != 1 || delivEnum[0] != "collect" {
			t.Fatalf("worker %s watch delivery enum = %v, want [collect]", profileID, delivEnum)
		}

		activeWatches = true
		liveMetas, _, _, err := loop.Context.coordinatorToolsForTurn(t.Context(), workerSess, profileID, nil, 0, 10, inject.CoordinatorTurnFrame{}, nil)
		testutil.FailErr(t, "worker tools live "+profileID, err)
		var controlSchema map[string]any
		for _, m := range liveMetas {
			if m.Name == "watch_control" {
				controlSchema = m.ArgsSchema
			}
		}
		if controlSchema == nil {
			t.Fatalf("worker %s with active watch must promote watch_control", profileID)
		}
		ctrlProps, _ := controlSchema["properties"].(map[string]any)
		ctrlDeliv, _ := ctrlProps["delivery"].(map[string]any)["enum"].([]any)
		if len(ctrlDeliv) != 1 || ctrlDeliv[0] != "collect" {
			t.Fatalf("worker %s watch_control delivery enum = %v, want [collect]", profileID, ctrlDeliv)
		}
	}

	// An unknown/ineligible profile without watch or watch_control never exposes them even with active watches.
	activeWatches = true
	ineligibleMetas, _, _, err := loop.Context.coordinatorToolsForTurn(t.Context(), workerSess, "ineligible_worker_no_watches", nil, 0, 10, inject.CoordinatorTurnFrame{}, nil)
	testutil.FailErr(t, "ineligible worker tools", err)
	for _, m := range ineligibleMetas {
		if m.Name == "watch" || m.Name == "watch_control" {
			t.Fatalf("ineligible worker offered %s", m.Name)
		}
	}
}
