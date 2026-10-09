package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompiledSurfaceExpandsCommandFamily(t *testing.T) {
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: toolcontract.SurfaceImplementInvestigate}, 1)
	testutil.FailErr(t, "compile tool plan", err)
	for _, name := range []string{"command", "command_output", "command_stop"} {
		if !plan.Deferred(name) {
			t.Fatalf("investigate compile missing deferred %s: %v", name, plan.AddressableNames())
		}
	}
}

func TestCompiledSurfaceLiveResourceAddsWaitOnInvestigate(t *testing.T) {
	loop := &PromptLoop{Deps: PromptLoopDeps{
		LiveResources: func(string) toolcontract.ResourcePresence {
			return toolcontract.ResourcePresence{CommandJobs: true}
		},
	}}
	sess := &api.Session{ID: "s1", WorkspacePath: "/tmp/repo"}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: toolcontract.SurfaceImplementInvestigate}, 1)
	testutil.FailErr(t, "compile tool plan", err)
	plan = modelTurn{loop}.compileRuntimeToolPlan(plan, api.CoordinatorRunContext{}, nil, nil, tools.MCPToolPlan{}, false, sess)
	for _, name := range []string{"command_output", "command_stop", "wait"} {
		if !plan.Immediate(name) {
			t.Fatalf("investigate + live jobs missing immediate %s: %v", name, plan.ImmediateNames())
		}
	}
}

func TestCompiledTurnPlanAdmitsActivatedOpenWorld(t *testing.T) {
	loop := &PromptLoop{Deps: PromptLoopDeps{
		LoadedTools: func(string) map[string]bool {
			return map[string]bool{"mcp_github_create_pr": true}
		},
	}}
	sess := &api.Session{ID: "s1", WorkspacePath: "/tmp/repo"}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_park"}, 1)
	testutil.FailErr(t, "compile tool plan", err)
	active := modelTurn{loop}.activeDeferredTools(sess)
	plan = modelTurn{loop}.compileRuntimeToolPlan(plan, api.CoordinatorRunContext{}, nil, active, tools.MCPToolPlan{}, false, sess)
	if !plan.Immediate("mcp_github_create_pr") {
		t.Fatalf("activated MCP must bypass surface: %v", plan.ImmediateNames())
	}
	if plan.Addressable("command_output") {
		t.Fatal("park without jobs must not admit command_output")
	}
}

func TestCompiledRootlessSurfaceRejectsRuntimeOpenWorldTools(t *testing.T) {
	plan := toolsurface.Compile([]string{"request_tools", "mcp_coropa_intel_search"}, []string{"write"})
	plan = filterToolPlanForRootCount(plan, 0)
	for _, name := range []string{"request_tools", "mcp_coropa_intel_search", "write"} {
		if plan.Addressable(name) {
			t.Fatalf("root-bound %s survived the rootless plan: %v", name, plan.AddressableNames())
		}
	}
}

func TestCoordinatorToolsForTurnDefersSmallAutomaticMCPSet(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register mcp", reg.RegisterDefinition(tools.Definition{
		Meta: tools.ToolMeta{
			Name: "mcp_coropa_intel_search", Deferred: true, Description: "Federated search",
			Source: tools.ToolSourceMCP, SourceID: "coropa", ArgsSchema: map[string]any{"type": "object"},
		},
		Contract: toolcontract.External("mcp:coropa"),
		Handler:  func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil },
	}))
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{
			{Name: "read"},
			{Name: "request_tools"},
			{Name: "mcp_coropa_intel_search", Deferred: true, Description: "Federated search", Source: tools.ToolSourceMCP, SourceID: "coropa"},
		}},
		Tools: reg,
	})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	got, plan := mustCoordinatorToolsForTurn(t, loop, sess, inject.CoordinatorTurnFrame{})
	if containsMeta(got, "mcp_coropa_intel_search") {
		t.Fatalf("automatic MCP must remain deferred by default: %v", namesOf(got))
	}
	if !plan.Deferred("mcp_coropa_intel_search") {
		t.Fatalf("automatic MCP must be requestable via request_tools: %v", plan.DeferredNames())
	}
}

// Live jobs expose their control tools.
func TestCoordinatorToolsForTurnPutsLiveJobControlsOnTheWire(t *testing.T) {
	metas := []tools.ToolMeta{
		{Name: "read"},
		{Name: "request_tools"},
		{Name: "command", Deferred: true},
		{Name: "command_output", Deferred: true},
		{Name: "command_stop", Deferred: true},
		{Name: "wait", Deferred: true},
	}
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}

	idle := NewPromptLoopForTest(PromptLoopDeps{Policy: registryTestPolicy{metas: metas}})
	got, _ := mustCoordinatorToolsForTurn(t, idle, sess, inject.CoordinatorTurnFrame{})
	if containsMeta(got, "command_output") {
		t.Fatalf("no live job should mean no control schema: %v", namesOf(got))
	}

	live := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: metas},
		LiveResources: func(string) toolcontract.ResourcePresence {
			return toolcontract.ResourcePresence{CommandJobs: true}
		},
	})
	got, _ = mustCoordinatorToolsForTurn(t, live, sess, inject.CoordinatorTurnFrame{})
	for _, name := range []string{"command_output", "command_stop", "wait"} {
		if !containsMeta(got, name) {
			t.Fatalf("a live job must put %s on the wire: %v", name, namesOf(got))
		}
	}
}

func TestCoordinatorToolsForTurnOffersSurfaceStickyProfileDeferredTool(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{
			{Name: "scan_list", Deferred: true},
			{Name: "request_tools"},
		}},
	})
	sess := &api.Session{
		ID: "s1", AgentType: prompts.CoordinatorProfileID,
		WorkspacePath: "/tmp/repo", Posture: api.SessionPostureVet,
	}
	frame := inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{
		WorkflowID: "security-survey", RunStatus: "running",
		PhaseCoordinatorSurface: "orchestrate_plan",
	}}
	got, plan := mustCoordinatorToolsForTurn(t, loop, sess, frame)
	if !containsMeta(got, "scan_list") {
		t.Fatalf("surface-sticky scan_list missing from provider tools: %v", namesOf(got))
	}
	if !plan.Immediate("scan_list") {
		t.Fatalf("scan_list plan mode = %v", plan.Availability("scan_list"))
	}
}

func TestEveryCoordinatorSurfaceProjectsItsCompiledImmediateSet(t *testing.T) {
	catalog, err := surfacecatalog.Load()
	testutil.FailErr(t, "load coordinator surfaces", err)
	for _, surfaceID := range catalog.SurfaceIDs() {
		t.Run(surfaceID, func(t *testing.T) {
			plan, planErr := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
			testutil.FailErr(t, "compile tool plan", planErr)
			metas := make([]tools.ToolMeta, 0, len(plan.AddressableNames()))
			for _, name := range plan.AddressableNames() {
				metas = append(metas, tools.ToolMeta{Name: name, Deferred: true})
			}
			offered := trimCoordinatorToolMetasForPlan(plan, metas, nil, nil)
			for _, name := range plan.ImmediateNames() {
				if !containsMeta(offered, name) {
					t.Fatalf("immediate tool %q omitted when profile metadata marks it deferred", name)
				}
			}
			for _, name := range plan.DeferredNames() {
				if containsMeta(offered, name) {
					t.Fatalf("deferred tool %q was offered before activation", name)
				}
			}
		})
	}
}

func TestCompiledTurnPlanIncludesMCPOnInvestigate(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register mcp", reg.RegisterDefinition(tools.Definition{
		Meta:     tools.ToolMeta{Name: "mcp_coropa_intel_search", Source: tools.ToolSourceMCP, SourceID: "coropa", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("mcp:coropa"),
		Handler:  func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil },
	}))
	loop := NewPromptLoopForTest(PromptLoopDeps{Tools: reg})
	sess := &api.Session{ID: "s1", WorkspacePath: "/tmp/repo"}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_investigate"}, 1)
	testutil.FailErr(t, "compile investigate plan", err)
	metas := reg.List()
	mcpPlan := modelTurn{loop}.mcpToolPlan(context.Background(), sess, metas, nil)
	plan = modelTurn{loop}.compileRuntimeToolPlan(plan, api.CoordinatorRunContext{}, metas, nil, mcpPlan, true, sess)
	if !plan.Addressable("mcp_coropa_intel_search") {
		t.Fatalf("investigate must allow MCP invoke without request_tools: %v", plan.AddressableNames())
	}
	park, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_park"}, 1)
	testutil.FailErr(t, "compile park plan", err)
	park = modelTurn{loop}.compileRuntimeToolPlan(park, api.CoordinatorRunContext{}, metas, nil, mcpPlan, false, sess)
	if park.Addressable("mcp_coropa_intel_search") {
		t.Fatalf("park must not allow MCP: %v", park.AddressableNames())
	}
}

func TestCoordinatorToolsForTurnDefersRegistryMCPMissingFromList(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register mcp", reg.RegisterDefinition(tools.Definition{
		Meta:     tools.ToolMeta{Name: "mcp_coropa_intel_search", Source: tools.ToolSourceMCP, SourceID: "coropa", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("mcp:coropa"),
		Handler:  func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil },
	}))
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}}},
		Tools:  reg,
	})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	got, plan := mustCoordinatorToolsForTurn(t, loop, sess, inject.CoordinatorTurnFrame{})
	if containsMeta(got, "mcp_coropa_intel_search") {
		t.Fatalf("automatic registry MCP must not be eager: %v", namesOf(got))
	}
	if !plan.Deferred("mcp_coropa_intel_search") {
		t.Fatalf("investigate must append registry MCP as deferred: %v", plan.DeferredNames())
	}
}

func TestCoordinatorToolsForTurnAlwaysLoadedRegistryMCPIsEager(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register mcp", reg.RegisterDefinition(tools.Definition{
		Meta:     tools.ToolMeta{Name: "mcp_coropa_intel_search", Source: tools.ToolSourceMCP, SourceID: "coropa", AlwaysLoad: true, ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("mcp:coropa"),
		Handler:  func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil },
	}))
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}}},
		Tools:  reg,
	})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	got, plan := mustCoordinatorToolsForTurn(t, loop, sess, inject.CoordinatorTurnFrame{})
	if !containsMeta(got, "mcp_coropa_intel_search") {
		t.Fatalf("always-loaded MCP must be eager in wire tools: %v", namesOf(got))
	}
	if !plan.Immediate("mcp_coropa_intel_search") {
		t.Fatalf("always-loaded MCP must be immediate in plan: %v", plan.AddressableNames())
	}
}

func TestCoordinatorToolsForTurnDefersAutomaticMCPSet(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{
			{Name: "request_tools"},
			{Name: "mcp_alpha_read", Source: tools.ToolSourceMCP},
			{Name: "mcp_beta_read", Source: tools.ToolSourceMCP},
		}},
	})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	got, plan := mustCoordinatorToolsForTurn(t, loop, sess, inject.CoordinatorTurnFrame{})
	if containsMeta(got, "mcp_alpha_read") || containsMeta(got, "mcp_beta_read") {
		t.Fatalf("automatic MCP set must remain deferred: %v", namesOf(got))
	}
	if !plan.Deferred("mcp_alpha_read") || !plan.Deferred("mcp_beta_read") {
		t.Fatalf("automatic MCP schemas must remain requestable: %v", plan.DeferredNames())
	}
}

func TestCoordinatorToolsForTurnIntersectsActiveSurface(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{
			{Name: "read"},
			{Name: "command"},
			{Name: "task"},
			{Name: "wait"},
		}},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{WorkersInFlight: 1}
		},
	})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	got, _ := mustCoordinatorToolsForTurn(t, loop, sess, inject.CoordinatorTurnFrame{})
	if containsMeta(got, "read") || containsMeta(got, "command") {
		t.Fatalf("park wire must not leak off-surface profile tools: %v", namesOf(got))
	}
	if !containsMeta(got, "wait") || !containsMeta(got, "task") {
		t.Fatalf("park wire missing coordination tools: %v", namesOf(got))
	}
}

func TestCoordinatorToolsForTurnHidesMCPOnPark(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{
			{Name: "read"},
			{Name: "mcp_coropa_intel_search", Source: tools.ToolSourceMCP, SourceID: "coropa"},
		}},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{WorkersInFlight: 1}
		},
	})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	got, _ := mustCoordinatorToolsForTurn(t, loop, sess, inject.CoordinatorTurnFrame{})
	if containsMeta(got, "mcp_coropa_intel_search") {
		t.Fatalf("park must not offer unactivated MCP: %v", namesOf(got))
	}
}

func TestCoordinatorToolsForTurnAppendsActivatedMissingFromList(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register mcp", reg.RegisterDefinition(tools.Definition{
		Meta:     tools.ToolMeta{Name: "mcp_coropa_intel_search", Description: "search", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("mcp:coropa"),
		Handler:  func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil },
	}))
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "read"}}},
		Tools:  reg,
		LoadedTools: func(string) map[string]bool {
			return map[string]bool{"mcp_coropa_intel_search": true}
		},
	})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	got, _ := mustCoordinatorToolsForTurn(t, loop, sess, inject.CoordinatorTurnFrame{})
	if !containsMeta(got, "mcp_coropa_intel_search") {
		t.Fatalf("activated MCP missing from ListForPrompt must still be offered: %v", namesOf(got))
	}
}

func namesOf(metas []tools.ToolMeta) []string {
	out := make([]string, 0, len(metas))
	for _, meta := range metas {
		out = append(out, meta.Name)
	}
	return out
}

func containsMeta(metas []tools.ToolMeta, name string) bool {
	for _, meta := range metas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

func TestCompiledTurnPlanUnknownSurfaceDenies(t *testing.T) {
	if surfaceAllowsTool(toolsurface.Compile(nil, nil), "not_a_surface", "command") {
		t.Fatal("unknown surface must deny")
	}
	if !surfaceAllowsTool(toolsurface.Plan{}, "", "command") {
		t.Fatal("no coordinator surface must leave worker invocation unconstrained")
	}
}

func mustCoordinatorToolsForTurn(
	t *testing.T,
	loop *PromptLoop,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
) ([]tools.ToolMeta, toolsurface.Plan) {
	t.Helper()
	metas, plan, _, err := modelTurn{loop}.coordinatorToolsForTurn(
		context.Background(), sess, prompts.CoordinatorProfileID, nil, "do work", 0, 8, frame, nil,
	)
	testutil.FailErr(t, "compile coordinator tools", err)
	return metas, plan
}
