package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/spawn"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestImplementManifestParallelTaskCapsMatchSpawnSSOT(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "implement", "workflows", "implement", "workflow.yaml"))
	if err != nil {
		t.Fatalf("read implement/workflow.yaml: %v", err)
	}
	manifest, err := workflowdef.ParseManifestYAML(data)
	if err != nil {
		t.Fatalf("ParseManifestYAML: %v", err)
	}
	for _, phaseID := range []string{"boot", "work"} {
		phase, ok := manifest.PhaseByID(phaseID)
		if !ok {
			t.Fatalf("missing phase %q", phaseID)
		}
		if phase.ParallelTask == nil {
			t.Fatalf("phase %q missing parallel_task", phaseID)
		}
		pt := phase.ParallelTask
		if pt.MaxWorkers != spawn.MaxInFlightTaskWorkers {
			t.Fatalf("%s max_workers = %d want %d", phaseID, pt.MaxWorkers, spawn.MaxInFlightTaskWorkers)
		}
		if pt.MaxReadWorkers != spawn.DefaultMaxReadTaskWorkers {
			t.Fatalf("%s max_read_workers = %d want %d", phaseID, pt.MaxReadWorkers, spawn.DefaultMaxReadTaskWorkers)
		}
		if pt.MaxWriteWorkers != spawn.DefaultMaxWriteTaskWorkers {
			t.Fatalf("%s max_write_workers = %d want %d", phaseID, pt.MaxWriteWorkers, spawn.DefaultMaxWriteTaskWorkers)
		}
	}
}

func TestImplementManifestAllowedAgentsIncludesSpawnSSOT(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "implement", "workflows", "implement", "workflow.yaml"))
	if err != nil {
		t.Fatalf("read implement/workflow.yaml: %v", err)
	}
	manifest, err := workflowdef.ParseManifestYAML(data)
	if err != nil {
		t.Fatalf("ParseManifestYAML: %v", err)
	}
	allowed := make(map[string]struct{}, len(manifest.AllowedAgents))
	for _, id := range manifest.AllowedAgents {
		allowed[id] = struct{}{}
	}
	for _, id := range spawn.AmbientAllowedAgents() {
		if _, ok := allowed[id]; !ok {
			t.Fatalf("implement.yaml allowed_agents missing spawn SSOT agent %q", id)
		}
	}
}

func TestCoordinatorImplementTripartiteWithAmbientRun(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	rendered := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{
		WorkflowID:      "implement",
		WorkflowVersion: "1.0.0",
		CurrentPhase:    "work",
	}, nil, surface.HostLoopWakeSentinel, WorkerCompletionHistory(orchestration.ProfileImplementer), "",
		surface.WithWrapupGates(surface.ImplementSessionState{}, true, false))
	for _, want := range []string{
		"Synthesis turn",
		"task()",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered playbook missing %q", want)
		}
	}
	renderedBoot := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{
		WorkflowID: "implement", RunID: "run-1", CurrentPhase: "boot",
	}, &api.Session{Posture: api.SessionPostureBuild}, "Map this codebase", nil, "")
	assertCoordinatorInvestigateBehavioral(t, renderedBoot)
	renderedOrchestrate := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{
		WorkflowID: "implement", RunID: "run-1", CurrentPhase: "boot",
	}, &api.Session{Posture: api.SessionPostureBuild},
		surface.HostLoopWakeSentinel, WorkerCompletionHistory(orchestration.ProfileImplementer), "",
		surface.WithWrapupGates(surface.ImplementSessionState{}, true, false))
	for _, want := range []string{
		"SYNTH_HANDLE_NOT_IN_LEGS",
		"SYNTH_CITATION_UNVERIFIABLE",
		"Read-only report",
	} {
		if !strings.Contains(renderedOrchestrate, want) {
			t.Fatalf("batch-ready orchestrate-lock render missing %q", want)
		}
	}
	for _, forbid := range []string{
		"INVEST_HANDLE_NOT_OBSERVED",
		"COORDINATOR_ORCHESTRATE_WRITE_DENIED",
	} {
		if strings.Contains(renderedOrchestrate, forbid) {
			t.Fatalf("batch-ready wrapup render must not contain investigate/orchestrate marker %q", forbid)
		}
	}
	spawnBlock := renderImplementSpawnInjectForContract(t, root)
	agents, err := agentdef.LoadEffective()
	if err != nil {
		t.Fatalf("LoadAgentProfilesEffective: %v", err)
	}
	byID := map[string]agentdef.Profile{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	for _, id := range spawn.AmbientAllowedAgents() {
		agent, ok := byID[id]
		if !ok {
			t.Fatalf("missing agent %q", id)
		}
		if !strings.Contains(spawnBlock, agent.Description) {
			t.Fatalf("spawn inject missing SSOT description for %q: %q", id, agent.Description)
		}
	}
	for _, want := range []string{
		"Spawn roster",
		"`implementer`",
		"`plan-writer`",
		"DISALLOWED_AGENT",
		"(no command)",
		"Budget:",
		"omit `max_tool_loops`",
	} {
		if !strings.Contains(spawnBlock, want) {
			t.Fatalf("implement-spawn inject missing %q", want)
		}
	}
	for _, forbid := range []string{
		"hotfix-template",
		"workflow_compose_from_template (hotfix",
	} {
		if strings.Contains(rendered, forbid) {
			t.Fatalf("implement-default playbook must not pressure %q", forbid)
		}
	}
	configRoot := filepath.Join(root, "lycaon")
	profile := surface.ResolveTurnProfile(surface.EnrichRunContextForWorkflow(api.CoordinatorRunContext{
		WorkflowID:      "implement",
		WorkflowVersion: "1.0.0",
		CurrentPhase:    "boot",
	}, configRoot), &api.Session{
		Posture: api.SessionPostureBuild,
	}, coordinatorTurnHistory(WorkerCompletionHistory(orchestration.ProfileImplementer), surface.HostLoopWakeSentinel), surface.WithWrapupGates(surface.ImplementSessionState{}, true, false))
	if profile.ModeRefs[0] != "implement-synthesis" {
		t.Fatalf("mode refs = %v want implement-synthesis for implement boot loop wake", profile.ModeRefs)
	}
	investigateProfile := surface.ResolveTurnProfile(surface.EnrichRunContextForWorkflow(api.CoordinatorRunContext{
		WorkflowID: "implement", CurrentPhase: "boot",
	}, configRoot), &api.Session{Posture: api.SessionPostureBuild}, coordinatorTurnHistory(nil, "Map this codebase"))
	if investigateProfile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("visible user boot surface = %q want investigate", investigateProfile.SurfaceID)
	}
}

func TestCoordinatorInvestigateSurfaceStateMatrix(t *testing.T) {
	t.Parallel()
	sess := &api.Session{Posture: api.SessionPostureBuild}
	cases := []struct {
		name     string
		runCtx   api.CoordinatorRunContext
		history  []api.Message
		prompt   string
		state    surface.ImplementSessionState
		rowSess  *api.Session
		wantSurf string
	}{
		{
			name:     "idle_visible_user",
			prompt:   "fix src/auth.go",
			wantSurf: toolcontract.SurfaceImplementInvestigate,
		},
		{
			name:     "workers_in_flight",
			prompt:   "fix src/auth.go",
			state:    surface.ImplementSessionState{WorkersInFlight: 1},
			wantSurf: surface.SurfaceImplementPark,
		},
		{
			name: "plan_catalog",
			runCtx: api.CoordinatorRunContext{
				WorkflowID: "plan", CurrentPhase: "research", SurfaceProfile: "plan",
			},
			prompt:   surface.HostLoopWakeSentinel,
			wantSurf: "plan_research",
		},
		{
			name: "workflow_orchestrate_dispatch",
			runCtx: api.CoordinatorRunContext{
				WorkflowDefaultExecutionMode: surface.ExecutionModeFamilyOrchestrate,
			},
			prompt:   "fix src/auth.go",
			rowSess:  &api.Session{Posture: api.SessionPostureBuild},
			wantSurf: surface.SurfaceImplementDispatch,
		},
		{
			name: "workflow_investigate",
			runCtx: api.CoordinatorRunContext{
				WorkflowDefaultExecutionMode: surface.ExecutionModeFamilyInvestigate,
			},
			prompt:   "fix src/auth.go",
			rowSess:  &api.Session{Posture: api.SessionPostureBuild},
			wantSurf: toolcontract.SurfaceImplementInvestigate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rowSess := sess
			if tc.rowSess != nil {
				rowSess = tc.rowSess
			}
			runCtx := surface.EnrichRunContextForWorkflow(tc.runCtx, filepath.Join(contractcheck.RepoRoot(t), "lycaon"))
			profile := surface.ResolveTurnProfile(runCtx, rowSess, coordinatorTurnHistory(tc.history, tc.prompt), tc.state)
			if profile.SurfaceID != tc.wantSurf {
				t.Fatalf("surface = %q want %q", profile.SurfaceID, tc.wantSurf)
			}
		})
	}
}
