package promptrender

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// Device packs that own a tool profile or the process tool schemas
// (docs/extend.md). Each case meets only the states where the guard reading
// the moved fact renders.
var _ = hoststate.AddCustomizationCases(deadBranchCases()...)

func deadBranchCases() []hoststate.CustomizationCase {
	return []hoststate.CustomizationCase{
		// Previews worker results and restores files, with read-only command,
		// no verify and no dispatch: the worker-finished kick's inspect arm
		// with worker_history, a surface card that may only edit, and a
		// rootless orchestrate turn without task.
		ownCoordinatorProfile("coordinator-without-landing", pendingOverlaySeed(), investigateAllLoadedSeed(), rootlessOrchestrateSeed()),
		// Previews and runs checks without worker_history, restores or
		// dispatch: the inspect arm without worker_history, and a card that
		// may only run.
		ownCoordinatorProfile("coordinator-preview-only", pendingOverlaySeed()),
		// Restores files and dispatches without verify or mutating command: a
		// surface card that may edit and dispatch without running commands.
		ownCoordinatorProfile("coordinator-edit-and-dispatch", investigateAllLoadedSeed()),
		// Holds scan_pack without scan_summary on a vet surface: witnesses
		// coordinator-surface-vet's else arm with scan_pack offered.
		ownCoordinatorProfile("coordinator-scan-pack-only", vetAllLoadedSeed()),
		// Loads scan_pack while scan_summary stays deferred on
		// observe_investigate: witnesses coordinator-surface-vet's elif arm
		// with scan_pack offered.
		ownCoordinatorProfile("coordinator-scan-pack-first", vetPartlyLoadedSeed()),
		{
			ID: "project-without-read-workers", Project: deadBranchFixture("without-read-workers"),
			Coordinator: func() ([]hoststate.CustomizationSeed, error) {
				return []hoststate.CustomizationSeed{investigateAllLoadedSeed()}, nil
			},
		},
		{
			ID: "own-explore-readonly-no-read-tools", Packs: []string{deadBranchFixture("packs", "explore-readonly-no-read-tools")},
			Own:          map[string]string{"tools/profiles/explore_readonly": "fixture/explore-readonly-no-read-tools"},
			WorkerAgents: []string{"code-reviewer"},
			Workers:      []hoststate.WorkerSeed{overrideWorkerSeed(hoststate.WorkerLoadingStickyOnly, false)},
		},
		{
			ID: "own-explore-readonly-no-tools", Packs: []string{deadBranchFixture("packs", "explore-readonly-no-tools")},
			Own:          map[string]string{"tools/profiles/explore_readonly": "fixture/explore-readonly-no-tools"},
			WorkerAgents: []string{"code-reviewer"},
			Workers:      []hoststate.WorkerSeed{overrideWorkerSeed(hoststate.WorkerLoadingStickyOnly, false)},
		},
		ownCoordinatorProfileWith("coordinator-secplan-a", func() ([]hoststate.CustomizationSeed, error) {
			return securitySurveyPhaseSeeds("plan")
		}),
		ownCoordinatorProfileWith("coordinator-secplan-b", func() ([]hoststate.CustomizationSeed, error) {
			return securitySurveyPhaseSeeds("plan")
		}),
		ownCoordinatorProfileWith("coordinator-secplan-c", func() ([]hoststate.CustomizationSeed, error) {
			return securitySurveyPhaseSeeds("plan", "execute")
		}),
		{
			ID: "own-implement-terminal-on-request", Packs: []string{deadBranchFixture("packs", "implement-terminal-on-request")},
			Own:          map[string]string{"tools/profiles/implement": "fixture/implement-terminal-on-request"},
			WorkerAgents: []string{"implementer"},
			Workers:      []hoststate.WorkerSeed{overrideWorkerSeed(hoststate.WorkerLoadingStickyOnly, false)},
		},
		{
			ID: "own-implement-checks-only", Packs: []string{deadBranchFixture("packs", "implement-checks-only")},
			Own:          map[string]string{"tools/profiles/implement": "fixture/implement-checks-only"},
			WorkerAgents: []string{"implementer"},
			Workers:      []hoststate.WorkerSeed{overrideWorkerSeed(hoststate.WorkerLoadingStickyOnly, false)},
		},
		{
			ID: "own-schemas-without-write-root", Packs: []string{deadBranchFixture("packs", "schemas-without-write-root")},
			Own: map[string]string{
				"tools/schemas/command": "fixture/schemas-without-write-root", "tools/schemas/verify": "fixture/schemas-without-write-root",
				"tools/schemas/terminal_open": "fixture/schemas-without-write-root",
			},
			Coordinator: func() ([]hoststate.CustomizationSeed, error) {
				return []hoststate.CustomizationSeed{investigateAllLoadedSeed()}, nil
			},
			WorkerAgents: []string{"implementer"},
			Workers:      []hoststate.WorkerSeed{overrideWorkerSeed(hoststate.WorkerLoadingAllLoaded, false)},
		},
	}
}

func ownCoordinatorProfile(pack string, seeds ...hoststate.CustomizationSeed) hoststate.CustomizationCase {
	return ownCoordinatorProfileWith(pack, func() ([]hoststate.CustomizationSeed, error) { return seeds, nil })
}

func ownCoordinatorProfileWith(pack string, seeds func() ([]hoststate.CustomizationSeed, error)) hoststate.CustomizationCase {
	return hoststate.CustomizationCase{
		ID: "own-" + pack, Packs: []string{deadBranchFixture("packs", pack)},
		Own:         map[string]string{"tools/profiles/coordinator": "fixture/" + pack},
		Coordinator: seeds,
	}
}

func securitySurveyPhaseSeeds(phaseIDs ...string) ([]hoststate.CustomizationSeed, error) {
	bindings, err := hoststate.ShippedPhaseBindings()
	if err != nil {
		return nil, err
	}
	var out []hoststate.CustomizationSeed
	for i := range bindings {
		b := &bindings[i]
		if b.WorkflowID != "security-survey" || !slices.Contains(phaseIDs, b.PhaseID) {
			continue
		}
		out = append(out, hoststate.CustomizationSeed{
			Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, Workflow: hoststate.WorkflowActivePhase,
			SurfaceID: b.Resolved.CoordinatorSurface, Roots: hoststate.RootsSingleGit, Loading: hoststate.LoadingFloorOnly,
			Binding: b,
		})
	}
	if len(out) != len(phaseIDs) {
		return nil, fmt.Errorf("security-survey phases %v: matched %d bindings", phaseIDs, len(out))
	}
	return out, nil
}

// pendingOverlaySeed resolves to the promotion surface, the only floor that
// lists preview_overlay.
func pendingOverlaySeed() hoststate.CustomizationSeed {
	return ambientSeed(api.SessionPostureBuild, hoststate.WorkflowPendingOverlays, hoststate.RootsSingleGit, hoststate.LoadingFloorOnly)
}

func investigateAllLoadedSeed() hoststate.CustomizationSeed {
	return ambientSeed(api.SessionPostureBuild, hoststate.WorkflowAmbientIdle, hoststate.RootsSingleGit, hoststate.LoadingAllLoaded)
}

func rootlessOrchestrateSeed() hoststate.CustomizationSeed {
	return ambientSeed(api.SessionPostureOrchestrate, hoststate.WorkflowWorkersInFlight, hoststate.RootsNone, hoststate.LoadingFloorOnly)
}

func vetAllLoadedSeed() hoststate.CustomizationSeed {
	return ambientSeed(api.SessionPostureVet, hoststate.WorkflowAmbientIdle, hoststate.RootsSingleGit, hoststate.LoadingAllLoaded)
}

func vetPartlyLoadedSeed() hoststate.CustomizationSeed {
	s := ambientSeed(api.SessionPostureVet, hoststate.WorkflowAmbientIdle, hoststate.RootsSingleGit, hoststate.LoadingPartlyLoaded)
	s.Loaded = []string{"scan_pack"}
	return s
}

func deadBranchFixture(parts ...string) string {
	return filepath.Join(append([]string{configlayout.FindModuleRoot(), "test", "contract", "invariants", "promptrender", "testdata", "ct09-deadbranches"}, parts...)...)
}
