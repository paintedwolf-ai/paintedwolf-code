package hoststate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerBranchView classifies a worker leg's repository and branch isolation view.
type WorkerBranchView string

const (
	BranchMainGit           WorkerBranchView = "main_git"
	BranchMainNonGit        WorkerBranchView = "main_nongit"
	BranchLinkedWorktreeGit WorkerBranchView = "linked_worktree_git"
	BranchIsolatedWorker    WorkerBranchView = "isolated_worker_branch"
)

// WorkerLoadingMode classifies sticky-only vs fully loaded deferred worker tools.
type WorkerLoadingMode string

const (
	WorkerLoadingStickyOnly WorkerLoadingMode = "sticky_only"
	WorkerLoadingAllLoaded  WorkerLoadingMode = "all_loaded"
)

// WorkerState captures one worker agent state across posture, branch, roots, and parent mode.
type WorkerState struct {
	ID                       string
	AgentID                  string
	ToolProfile              string
	ExecMode                 sandbox.ExecMode
	Posture                  api.SessionPosture
	BranchView               WorkerBranchView
	RootCount                int
	SecondaryRootGit         bool
	Loading                  WorkerLoadingMode
	ParentSessionMode        api.SessionMode
	ParentUpgradedAskToAgent bool
	WebSearch                bool
	Vision                   bool
	CommandExternalHidden    bool
	ApprovalPosture          gate.Posture

	HasGit         bool
	HasAnyGitRoot  bool
	OnWorkerBranch bool
	Loaded         map[string]bool
	Overlay        PackOverlay
	Excluded       bool
	ExcludedReason string
}

// WorkerStates generates the deterministic worker matrix across catalog worker agents,
// postures, branch views, root counts, loading states, and Ask-to-Agent parent upgrades.
func WorkerStates(ctx context.Context) ([]WorkerState, error) {
	overlays, err := DiscoverPackOverlays(ctx)
	if err != nil {
		return nil, err
	}
	stock := overlays[0]
	postures, err := Postures(stock.Catalog)
	if err != nil {
		return nil, err
	}
	profileMap := make(map[string]sandbox.ToolProfile, len(stock.Profiles))
	for _, p := range stock.Profiles {
		profileMap[p.ID] = p
	}

	branches := []WorkerBranchView{BranchMainGit, BranchMainNonGit, BranchLinkedWorktreeGit, BranchIsolatedWorker}
	loadings := []WorkerLoadingMode{WorkerLoadingStickyOnly, WorkerLoadingAllLoaded}
	var out []WorkerState
	for _, agent := range stock.WorkerAgents {
		prof := profileMap[agent.ToolProfile]
		for _, posture := range postures {
			for _, bv := range branches {
				for _, rc := range []int{1, 2} {
					for _, ld := range loadings {
						for _, parentAsk := range []bool{false, true} {
							for _, upgraded := range []bool{false, true} {
								if !parentAsk && upgraded {
									continue
								}
								out = append(out, buildWorkerState(stock, agent, prof, posture.ID, bv, rc, ld, parentAsk, upgraded))
							}
						}
					}
				}
			}
		}
	}
	return out, nil
}

func buildWorkerState(
	overlay PackOverlay,
	agent WorkerAgent,
	prof sandbox.ToolProfile,
	posture api.SessionPosture,
	bv WorkerBranchView,
	rootCount int,
	loading WorkerLoadingMode,
	parentAsk, upgraded bool,
) WorkerState {
	parentMode := api.SessionModeAgent
	if parentAsk {
		parentMode = api.SessionModeAsk
	}
	hasGit := bv != BranchMainNonGit
	secondaryGit := rootCount == 2 && bv == BranchMainNonGit
	anyGit := hasGit || secondaryGit
	onBranch := bv == BranchIsolatedWorker

	excluded := ""
	mutCapable, _ := prompts.AgentMutationCapable(agent.ID)
	if onBranch && !mutCapable {
		excluded = "read_only_agent_has_no_isolated_worker_branch"
	}
	if excluded == "" && parentAsk && onBranch {
		excluded = "ask_mode_never_spawns_isolated_branch_worker"
	}
	loaded := make(map[string]bool)
	if loading == WorkerLoadingAllLoaded {
		if len(prof.DeferredTools) == 0 {
			if excluded == "" {
				excluded = "worker_profile_has_no_deferred_tools"
			}
		} else {
			for name := range prof.DeferredTools {
				if !strings.HasSuffix(name, "*") {
					loaded[name] = true
				}
			}
		}
	}

	id := fmt.Sprintf(
		"worker/%s/posture=%s/branch=%s/roots=%d/load=%s/parent=%s/upgraded=%t",
		agent.ID, posture, bv, rootCount, loading, parentMode, upgraded,
	)
	return WorkerState{
		ID: id, AgentID: agent.ID, ToolProfile: agent.ToolProfile, ExecMode: agent.ExecMode,
		Posture: posture, BranchView: bv, RootCount: rootCount, SecondaryRootGit: secondaryGit,
		Loading: loading, ParentSessionMode: parentMode, ParentUpgradedAskToAgent: upgraded,
		WebSearch: true, ApprovalPosture: gate.PostureBalanced,
		HasGit: hasGit, HasAnyGitRoot: anyGit, OnWorkerBranch: onBranch,
		Loaded: loaded, Overlay: overlay,
		Excluded: excluded != "", ExcludedReason: excluded,
	}
}

func buildRootsForTopology(rt RootTopology) (roots []RootSpec, primaryGit, anyGit bool) {
	r1 := projectroot.RootRef{ID: "r1", Label: "primary", Path: "/tmp/fixture-primary", IsPrimary: true}
	r2 := projectroot.RootRef{ID: "r2", Label: "secondary", Path: "/tmp/fixture-secondary", IsPrimary: false}
	switch rt {
	case RootsSingleGit:
		return []RootSpec{{Ref: r1, HasGit: true}}, true, true
	case RootsSingleNonGit:
		return []RootSpec{{Ref: r1, HasGit: false}}, false, false
	case RootsDualGitFirst:
		return []RootSpec{{Ref: r1, HasGit: true}, {Ref: r2, HasGit: false}}, true, true
	case RootsDualNonGitFirst:
		return []RootSpec{{Ref: r1, HasGit: false}, {Ref: r2, HasGit: true}}, false, true
	default:
		return nil, false, false
	}
}

func buildTempPaths(ts TempState) (scratch string, shared []string) {
	switch ts {
	case TempScratchOnly:
		return "/tmp/pw-session-scratch", nil
	case TempScratchAndShared:
		return "/tmp/pw-session-scratch", []string{"/tmp/pw-shared-cache"}
	default:
		return "", nil
	}
}

func selectLoadedTools(plan toolsurface.Plan, mode LoadingMode) (map[string]bool, string) {
	deferred := plan.DeferredNames()
	slices.Sort(deferred)
	switch mode {
	case LoadingFloorOnly:
		return map[string]bool{}, ""
	case LoadingPartlyLoaded:
		if len(deferred) < 2 {
			return nil, "partly_loaded_requires_at_least_two_deferred_tools"
		}
		return map[string]bool{deferred[0]: true}, ""
	case LoadingAllLoaded:
		if len(deferred) == 0 {
			return nil, "all_loaded_redundant_on_closed_floor_surface"
		}
		out := make(map[string]bool, len(deferred))
		for _, name := range deferred {
			out[name] = true
		}
		return out, ""
	default:
		return map[string]bool{}, ""
	}
}
