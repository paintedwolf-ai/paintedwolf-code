package hoststate

import (
	"context"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/projectroot"
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

// WorkerLoadingMode classifies sticky-only, partially loaded, or fully loaded deferred worker tools.
type WorkerLoadingMode string

const (
	WorkerLoadingStickyOnly   WorkerLoadingMode = "sticky_only"
	WorkerLoadingPartlyLoaded WorkerLoadingMode = "partly_loaded"
	WorkerLoadingAllLoaded    WorkerLoadingMode = "all_loaded"
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
	TempState                TempState
	// MutationCapable is whether the agent's tool profile in Overlay can mutate.
	MutationCapable bool

	HasGit         bool
	HasAnyGitRoot  bool
	OnWorkerBranch bool
	// SessionScratchDir and SharedTempLocations are the child session's
	// temporary storage TempState selects.
	SessionScratchDir   string
	SharedTempLocations []string
	Loaded              map[string]bool
	Overlay             PackOverlay
	Excluded            bool
	ExcludedReason      string
}

// WorkerStates generates the deterministic worker matrix across catalog worker agents,
// postures, branch views, root Git topologies, loading states, Ask-to-Agent parent
// upgrades, and narrow environment/overlay variants via ResolveWorkerSeed.
func WorkerStates(ctx context.Context) ([]WorkerState, error) {
	domains, err := DefaultSeedDomains()
	if err != nil {
		return nil, err
	}
	stock := domains.Overlays[0]
	postures := domains.Postures

	branches := []WorkerBranchView{BranchMainGit, BranchMainNonGit, BranchLinkedWorktreeGit, BranchIsolatedWorker}
	loadings := []WorkerLoadingMode{WorkerLoadingStickyOnly, WorkerLoadingAllLoaded}
	modes := api.AllSessionModeValues()
	seen := make(map[string]bool, 1024)
	var out []WorkerState
	appendUnique := func(ws WorkerState) {
		if seen[ws.ID] {
			return
		}
		seen[ws.ID] = true
		out = append(out, ws)
	}

	for _, agent := range stock.WorkerAgents {
		for _, posture := range postures {
			for _, bv := range branches {
				for _, rc := range []int{1, 2} {
					secOptions := []bool{false}
					if rc == 2 && bv == BranchMainNonGit {
						secOptions = []bool{false, true}
					}
					for _, secGit := range secOptions {
						for _, ld := range loadings {
							for _, parentMode := range modes {
								for _, upgraded := range []bool{false, true} {
									if parentMode != api.SessionModeAsk && upgraded {
										continue
									}
									ws, buildErr := BuildWorkerState(ctx, stock, agent, WorkerSeed{
										Posture: posture.ID, BranchView: bv, RootCount: rc, SecondaryRootGit: secGit,
										Loading: ld, ParentSessionMode: parentMode, ParentUpgradedAskToAgent: upgraded,
										WebSearch: true, ApprovalPosture: gate.PostureBalanced, TempState: TempNone,
									})
									if buildErr != nil {
										return nil, buildErr
									}
									appendUnique(ws)
								}
							}
						}
					}
				}
			}
		}
	}

	narrowSeeds := buildNarrowWorkerSeeds(ctx, domains)
	for _, seed := range narrowSeeds {
		ws, rErr := ResolveWorkerSeed(ctx, domains, seed)
		if rErr != nil {
			return nil, rErr
		}
		appendUnique(ws)
	}
	custom, err := CustomizationOverlays(ctx)
	if err != nil {
		return nil, err
	}
	for _, co := range custom {
		if !co.Worker {
			continue
		}
		for _, agent := range co.Workers {
			for _, seed := range customizationWorkerSeeds() {
				ws, buildErr := BuildWorkerState(ctx, co.Overlay, agent, seed)
				if buildErr != nil {
					return nil, buildErr
				}
				appendUnique(ws)
			}
		}
	}
	return out, nil
}

// customizationWorkerSeeds meets a customized worker with both command-effect
// families, both parent modes, both loadings and an isolated branch.
func customizationWorkerSeeds() []WorkerSeed {
	var seeds []WorkerSeed
	for _, posture := range []api.SessionPosture{api.SessionPostureBuild, api.SessionPostureSpec} {
		for _, bv := range []WorkerBranchView{BranchMainGit, BranchIsolatedWorker} {
			for _, ld := range []WorkerLoadingMode{WorkerLoadingStickyOnly, WorkerLoadingAllLoaded} {
				for _, parentMode := range api.AllSessionModeValues() {
					seeds = append(seeds, WorkerSeed{
						Posture: posture, BranchView: bv, RootCount: 1, Loading: ld, ParentSessionMode: parentMode,
						WebSearch: true, ApprovalPosture: gate.PostureBalanced, TempState: TempNone,
					})
				}
			}
		}
	}
	return seeds
}

func buildNarrowWorkerSeeds(ctx context.Context, domains *SeedDomains) []WorkerSeed {
	baseMutating := WorkerSeed{
		OverlayIndex:      0,
		AgentID:           "implementer",
		Posture:           api.SessionPostureBuild,
		BranchView:        BranchMainGit,
		RootCount:         1,
		Loading:           WorkerLoadingAllLoaded,
		ParentSessionMode: api.SessionModeAgent,
		WebSearch:         true,
		ApprovalPosture:   gate.PostureBalanced,
		TempState:         TempNone,
	}
	baseReadOnly := WorkerSeed{
		OverlayIndex:      0,
		AgentID:           "repo-researcher",
		Posture:           api.SessionPostureSpec,
		BranchView:        BranchMainGit,
		RootCount:         1,
		Loading:           WorkerLoadingStickyOnly,
		ParentSessionMode: api.SessionModeAsk,
		WebSearch:         true,
		ApprovalPosture:   gate.PostureBalanced,
		TempState:         TempNone,
	}

	var seeds []WorkerSeed
	for _, app := range gate.AllPostures() {
		s1 := baseMutating
		s1.ApprovalPosture = app
		seeds = append(seeds, s1)

		s2 := baseReadOnly
		s2.ApprovalPosture = app
		seeds = append(seeds, s2)
	}

	// Non-external worker with web search disabled vs external worker exclusion.
	webOffMut := baseMutating
	webOffMut.WebSearch = false
	seeds = append(seeds, webOffMut)

	webOffExt := baseMutating
	webOffExt.AgentID = "web-researcher"
	webOffExt.Loading = WorkerLoadingStickyOnly
	webOffExt.WebSearch = false
	seeds = append(seeds, webOffExt)

	// Vision enabled on a worker with capture_page offered (flips claim-evidence visual self-review).
	visOn := baseMutating
	visOn.Vision = true
	seeds = append(seeds, visOn)

	// Explicit external selector hidden on a worker holding external exec.
	extHidden := baseMutating
	extHidden.CommandExternalHidden = true
	seeds = append(seeds, extHidden)

	// A spawned child has a scratch folder and may hold shared temp.
	for _, ts := range []TempState{TempScratchOnly, TempScratchAndShared} {
		withTemp := baseMutating
		withTemp.TempState = ts
		seeds = append(seeds, withTemp)
	}

	// A read-scope worker may run without project roots; a write-scope one may not.
	for _, agentID := range []string{"repo-researcher", "web-researcher", "implementer"} {
		rootless := baseReadOnly
		rootless.AgentID, rootless.RootCount, rootless.BranchView = agentID, 0, BranchMainNonGit
		rootless.ParentSessionMode = api.SessionModeAgent
		seeds = append(seeds, rootless)
	}

	// Partial deferred-tool loading: feasible >=2-candidate worker + <2-candidate exclusion.
	stock := domains.Overlays[0]
	addedFeasiblePartial := false
	addedExcludedPartial := false
	for _, ag := range stock.WorkerAgents {
		defs, err := admissibleWorkerDeferredNames(
			ctx, stock, ag.ID, ag.ToolProfile,
			api.SessionPostureBuild, api.SessionModeAgent, 1, true, false,
		)
		if err != nil {
			continue
		}
		if len(defs) >= 2 && !addedFeasiblePartial {
			s := baseMutating
			s.AgentID = ag.ID
			s.Loading = WorkerLoadingPartlyLoaded
			seeds = append(seeds, s)
			addedFeasiblePartial = true
		} else if len(defs) < 2 && !addedExcludedPartial {
			s := baseMutating
			s.AgentID = ag.ID
			s.Loading = WorkerLoadingPartlyLoaded
			seeds = append(seeds, s)
			addedExcludedPartial = true
		}
	}

	// Non-stock overlays on eligible worker agents (plus absent-worker exclusion where removed).
	for ovIdx := 1; ovIdx < len(domains.Overlays); ovIdx++ {
		ov := domains.Overlays[ovIdx]
		if len(ov.WorkerAgents) > 0 {
			s := baseReadOnly
			s.OverlayIndex = ovIdx
			s.AgentID = ov.WorkerAgents[0].ID
			s.Posture = api.SessionPostureVet
			s.ParentSessionMode = api.SessionModeAgent
			seeds = append(seeds, s)
		}
		hasImpl := false
		for _, w := range ov.WorkerAgents {
			if w.ID == "implementer" {
				hasImpl = true
				break
			}
		}
		if !hasImpl {
			missing := baseMutating
			missing.OverlayIndex = ovIdx
			seeds = append(seeds, missing)
		}
	}
	return seeds
}

// BuildWorkerState resolves seed's coordinates for agent in overlay.
func BuildWorkerState(ctx context.Context, overlay PackOverlay, agent WorkerAgent, seed WorkerSeed) (WorkerState, error) {
	bv, rootCount := seed.BranchView, seed.RootCount
	parentAsk := seed.ParentSessionMode == api.SessionModeAsk
	hasGit := rootCount > 0 && bv != BranchMainNonGit
	anyGit := hasGit || (rootCount >= 2 && seed.SecondaryRootGit)
	onBranch := bv == BranchIsolatedWorker

	excluded := ""
	mutCapable := profileMutationCapable(overlay, agent.ToolProfile)
	switch {
	case rootCount < 2 && seed.SecondaryRootGit:
		excluded = "secondary_git_requires_multiple_roots"
	case rootCount == 0 && bv != BranchMainNonGit:
		excluded = "worker_without_roots_has_no_repository_view"
	case rootCount == 0 && mutCapable:
		excluded = "write_scope_requires_project_roots"
	case onBranch && !mutCapable:
		excluded = "read_only_agent_has_no_isolated_worker_branch"
	case parentAsk && mutCapable:
		excluded = "ask_mode_forbids_mutating_worker"
	case parentAsk && onBranch:
		excluded = "ask_mode_never_spawns_isolated_branch_worker"
	case !seed.WebSearch && slices.Contains(agent.Profile.Capabilities, agentdef.CapabilityExternal):
		excluded = "web_search_disabled_for_external_agent"
	}

	loaded := make(map[string]bool)
	if seed.Loading == WorkerLoadingPartlyLoaded || seed.Loading == WorkerLoadingAllLoaded {
		deferred, err := admissibleWorkerDeferredNames(
			ctx, overlay, agent.ID, agent.ToolProfile,
			seed.Posture, seed.ParentSessionMode, rootCount, seed.WebSearch, seed.CommandExternalHidden,
		)
		if err != nil {
			return WorkerState{}, err
		}
		switch {
		case seed.Loading == WorkerLoadingPartlyLoaded && len(deferred) < 2:
			if excluded == "" {
				excluded = "worker_partly_loaded_requires_at_least_two_deferred_tools"
			}
		case seed.Loading == WorkerLoadingPartlyLoaded:
			loaded[deferred[0]] = true
		case len(deferred) == 0:
			if excluded == "" {
				excluded = "worker_profile_has_no_deferred_tools"
			}
		default:
			for _, name := range deferred {
				loaded[name] = true
			}
		}
	}
	scratch, shared := buildTempPaths(seed.TempState)

	id := fmt.Sprintf(
		"worker/%s/posture=%s/branch=%s/roots=%d/secGit=%t/load=%s/parent=%s/upgraded=%t/ov=%s/web=%t/vis=%t/extH=%t/app=%s/temp=%s",
		agent.ID, seed.Posture, bv, rootCount, seed.SecondaryRootGit, seed.Loading, seed.ParentSessionMode, seed.ParentUpgradedAskToAgent,
		overlay.ID, seed.WebSearch, seed.Vision, seed.CommandExternalHidden, seed.ApprovalPosture, seed.TempState,
	)
	return WorkerState{
		ID: id, AgentID: agent.ID, ToolProfile: agent.ToolProfile, ExecMode: agent.ExecMode,
		Posture: seed.Posture, BranchView: bv, RootCount: rootCount, SecondaryRootGit: seed.SecondaryRootGit,
		Loading: seed.Loading, ParentSessionMode: seed.ParentSessionMode, ParentUpgradedAskToAgent: seed.ParentUpgradedAskToAgent,
		WebSearch: seed.WebSearch, Vision: seed.Vision, CommandExternalHidden: seed.CommandExternalHidden,
		ApprovalPosture: seed.ApprovalPosture, TempState: seed.TempState, MutationCapable: mutCapable,
		HasGit: hasGit, HasAnyGitRoot: anyGit, OnWorkerBranch: onBranch,
		SessionScratchDir: scratch, SharedTempLocations: shared,
		Loaded: loaded, Overlay: overlay,
		Excluded: excluded != "", ExcludedReason: excluded,
	}, nil
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
	case RootsDualNeitherGit:
		return []RootSpec{{Ref: r1, HasGit: false}, {Ref: r2, HasGit: false}}, false, false
	default:
		return nil, false, false
	}
}

func buildTempPaths(ts TempState) (scratch string, shared []string) {
	switch ts {
	case TempScratchOnly:
		return "/tmp/pw-session-scratch", nil
	case TempScratchAndShared:
		// A grant over every shared temp location the host knows.
		return "/tmp/pw-session-scratch", confine.WritableSharedTempLocations("", confine.SharedTempLocations())
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
