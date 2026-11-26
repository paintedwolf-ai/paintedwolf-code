package promptrender

import (
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// Unit override and disable cases. Each is a desired state Resolve admits:
// skills are device-only units, so their disables are device desired state;
// agents and prompt units are shared kinds a project may disable from its
// overlay extensions file (docs/extend.md, docs/project-overlay.md); tool
// schemas are device units a pack installed from a folder may win through
// own. Each case
// meets only the states where a guard reading the moved fact renders.
var _ = hoststate.AddCustomizationCases(overrideCases()...)

// visualSkills are the stock skills claim-evidence names per claim row.
var visualSkills = []string{
	"skills/mock-before-build",
	"skills/craft-icons-and-chrome",
	"skills/verify-visual-change",
	"skills/verify-terminal-change",
}

func overrideCases() []hoststate.CustomizationCase {
	var cases []hoststate.CustomizationCase
	// One skill left, and none: each skill atom of claim-evidence's
	// disjunctions flips with the others false.
	for _, kept := range append(slices.Clone(visualSkills), "") {
		id := "only-" + filepath.Base(kept)
		if kept == "" {
			id = "without-visual-skills"
		}
		cases = append(cases, hoststate.CustomizationCase{
			ID:             id,
			DeviceDisabled: slices.DeleteFunc(slices.Clone(visualSkills), func(s string) bool { return s == kept }),
			Coordinator:    skillReaderSeeds,
			WorkerAgents:   []string{"implementer", "plan-writer", "code-reviewer", "web-researcher"},
			Workers:        skillWorkerSeeds(),
		})
	}
	cases = append(cases,
		hoststate.CustomizationCase{
			ID: "project-without-web-researcher", Project: overrideFixture("without-web-researcher"),
			Coordinator: zeroRootSeeds,
		},
		hoststate.CustomizationCase{
			ID: "project-without-implementer", Project: overrideFixture("without-implementer"),
			Coordinator: requestableTaskSeeds,
		},
		hoststate.CustomizationCase{
			ID: "project-without-orientation-units", Project: overrideFixture("without-orientation-units"),
			Coordinator:  ambientBuildSeeds,
			WorkerAgents: []string{"code-reviewer", "skeptic", "plan-reviewer"},
			Workers:      []hoststate.WorkerSeed{overrideWorkerSeed(hoststate.WorkerLoadingStickyOnly, false)},
		},
	)
	// A device pack wins the process tool schemas without one or both local
	// service fields, so each field's guards flip with the other held.
	for _, field := range []string{"local-listen", "loopback-connect", "local-services"} {
		pack := "fixture/schemas-without-" + field
		own := map[string]string{}
		for _, tool := range []string{"command", "verify", "terminal_open"} {
			own["tools/schemas/"+tool] = pack
		}
		cases = append(cases, hoststate.CustomizationCase{
			ID: "own-schemas-without-" + field, Packs: []string{overrideFixture("packs", "schemas-without-"+field)}, Own: own,
			Coordinator:  ambientBuildSeeds,
			WorkerAgents: []string{"implementer"},
			Workers:      []hoststate.WorkerSeed{overrideWorkerSeed(hoststate.WorkerLoadingAllLoaded, false)},
		})
	}
	return cases
}

func overrideFixture(parts ...string) string {
	return filepath.Join(append([]string{configlayout.FindModuleRoot(), "test", "contract", "invariants", "promptrender", "testdata", "ct09-overrides"}, parts...)...)
}

func overrideWorkerSeed(loading hoststate.WorkerLoadingMode, vision bool) hoststate.WorkerSeed {
	return hoststate.WorkerSeed{
		Posture: api.SessionPostureBuild, BranchView: hoststate.BranchMainGit, RootCount: 1, Loading: loading,
		ParentSessionMode: api.SessionModeAgent, WebSearch: true, Vision: vision,
		ApprovalPosture: gate.PostureBalanced, TempState: hoststate.TempNone,
	}
}

// skillWorkerSeeds load every deferred tool with vision on, so each tool row
// of claim-evidence renders, and keep only sticky tools, where a skill is
// admitted through a requestable tool.
func skillWorkerSeeds() []hoststate.WorkerSeed {
	return []hoststate.WorkerSeed{
		overrideWorkerSeed(hoststate.WorkerLoadingAllLoaded, true),
		overrideWorkerSeed(hoststate.WorkerLoadingStickyOnly, false),
	}
}

func ambientSeed(posture api.SessionPosture, wf hoststate.WorkflowState, roots hoststate.RootTopology, loading hoststate.LoadingMode) hoststate.CustomizationSeed {
	return hoststate.CustomizationSeed{Mode: api.SessionModeAgent, Posture: posture, Workflow: wf, Roots: roots, Loading: loading}
}

func ambientBuildSeeds() ([]hoststate.CustomizationSeed, error) {
	return []hoststate.CustomizationSeed{
		ambientSeed(api.SessionPostureBuild, hoststate.WorkflowAmbientIdle, hoststate.RootsSingleGit, hoststate.LoadingFloorOnly),
		ambientSeed(api.SessionPostureBuild, hoststate.WorkflowAmbientIdle, hoststate.RootsSingleGit, hoststate.LoadingAllLoaded),
	}, nil
}

// skillReaderSeeds are the ambient coordinator turns whose surfaces offer or
// load the visual tools the four skills name.
func skillReaderSeeds() ([]hoststate.CustomizationSeed, error) {
	var out []hoststate.CustomizationSeed
	for _, p := range []api.SessionPosture{api.SessionPostureBuild, api.SessionPostureOrchestrate} {
		for _, wf := range []hoststate.WorkflowState{hoststate.WorkflowAmbientIdle, hoststate.WorkflowWorkersInFlight} {
			for _, roots := range []hoststate.RootTopology{hoststate.RootsNone, hoststate.RootsSingleGit} {
				out = append(out, ambientSeed(p, wf, roots, hoststate.LoadingAllLoaded))
			}
		}
	}
	vision := ambientSeed(api.SessionPostureBuild, hoststate.WorkflowAmbientIdle, hoststate.RootsSingleGit, hoststate.LoadingAllLoaded)
	vision.Vision = true
	return append(out, vision,
		ambientSeed(api.SessionPostureBuild, hoststate.WorkflowAmbientIdle, hoststate.RootsSingleGit, hoststate.LoadingFloorOnly)), nil
}

// zeroRootSeeds are folderless turns, where web-researcher is the only
// worker a coordinator may spawn: ambient, every folderless surface, and
// every shipped and project phase binding.
func zeroRootSeeds() ([]hoststate.CustomizationSeed, error) {
	folderless, err := hoststate.FolderlessSurfaces()
	if err != nil {
		return nil, err
	}
	shipped, err := hoststate.ShippedPhaseBindings()
	if err != nil {
		return nil, err
	}
	project, err := hoststate.ProjectPhaseBindings()
	if err != nil {
		return nil, err
	}
	out := []hoststate.CustomizationSeed{
		ambientSeed(api.SessionPostureBuild, hoststate.WorkflowAmbientIdle, hoststate.RootsNone, hoststate.LoadingAllLoaded),
		ambientSeed(api.SessionPostureOrchestrate, hoststate.WorkflowAmbientIdle, hoststate.RootsNone, hoststate.LoadingAllLoaded),
	}
	for _, surfaceID := range folderless {
		for _, mode := range api.AllSessionModeValues() {
			out = append(out, hoststate.CustomizationSeed{
				Mode: mode, Posture: api.SessionPostureBuild, Workflow: hoststate.WorkflowActivePhase,
				SurfaceID: surfaceID, Roots: hoststate.RootsNone, Loading: hoststate.LoadingAllLoaded,
			})
		}
	}
	bindings := slices.Concat(shipped, project)
	for i := range bindings {
		b := &bindings[i]
		for _, mode := range api.AllSessionModeValues() {
			out = append(out, hoststate.CustomizationSeed{
				Mode: mode, Posture: api.SessionPostureBuild, Workflow: hoststate.WorkflowActivePhase,
				SurfaceID: b.Resolved.CoordinatorSurface, Roots: hoststate.RootsNone, Loading: hoststate.LoadingAllLoaded, Binding: b,
			})
		}
	}
	return append(out, synthesisSeeds()...), nil
}

// synthesisSeeds are the report turns whose surface template states who may
// be spawned when no file tools are offered: the synthesis surface across
// modes, postures, root topologies and loadings.
func synthesisSeeds() []hoststate.CustomizationSeed {
	var out []hoststate.CustomizationSeed
	for _, mode := range api.AllSessionModeValues() {
		for _, p := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureBuild, api.SessionPostureOrchestrate, api.SessionPostureVet} {
			for _, roots := range []hoststate.RootTopology{hoststate.RootsNone, hoststate.RootsSingleGit, hoststate.RootsDualNonGitFirst} {
				for _, ld := range []hoststate.LoadingMode{hoststate.LoadingFloorOnly, hoststate.LoadingPartlyLoaded, hoststate.LoadingAllLoaded} {
					out = append(out, hoststate.CustomizationSeed{
						Mode: mode, Posture: p, Workflow: hoststate.WorkflowActivePhase,
						SurfaceID: spawn.SurfaceImplementSynthesis, Roots: roots, Loading: ld,
					})
				}
			}
		}
	}
	return out
}

// requestableTaskSeeds are the turns where task is loadable rather than
// held: every surface with only its floor loaded.
func requestableTaskSeeds() ([]hoststate.CustomizationSeed, error) {
	surfaces, err := hoststate.CoordinatorSurfaces()
	if err != nil {
		return nil, err
	}
	out, _ := ambientBuildSeeds()
	for _, s := range surfaces {
		out = append(out, hoststate.CustomizationSeed{
			Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, Workflow: hoststate.WorkflowActivePhase,
			SurfaceID: s.ID, Roots: hoststate.RootsSingleGit, Loading: hoststate.LoadingFloorOnly,
		})
	}
	return out, nil
}
