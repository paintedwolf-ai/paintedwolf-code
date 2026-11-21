package hoststate

import (
	"context"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowState classifies the coordinator's workflow or turn lifecycle slice.
type WorkflowState string

const (
	WorkflowAmbientIdle     WorkflowState = "ambient_idle"
	WorkflowWorkersInFlight WorkflowState = "workers_in_flight"
	WorkflowPendingOverlays WorkflowState = "pending_overlays"
	WorkflowActivePhase     WorkflowState = "active_phase"
)

// RootTopology classifies workspace folder attachment and Git root ordering.
type RootTopology string

const (
	RootsNone            RootTopology = "none"
	RootsSingleGit       RootTopology = "single_git"
	RootsSingleNonGit    RootTopology = "single_nongit"
	RootsDualGitFirst    RootTopology = "dual_git_first"
	RootsDualNonGitFirst RootTopology = "dual_nongit_first"
)

// LoadingMode classifies deferred-tool schema activation on a turn.
type LoadingMode string

const (
	LoadingFloorOnly    LoadingMode = "floor_only"
	LoadingPartlyLoaded LoadingMode = "partly_loaded"
	LoadingAllLoaded    LoadingMode = "all_loaded"
)

// TempState classifies session scratch and shared temporary directory availability.
type TempState string

const (
	TempNone             TempState = "none"
	TempScratchOnly      TempState = "scratch_only"
	TempScratchAndShared TempState = "scratch_and_shared"
)

// MCPState classifies attached MCP tool declarations on the turn.
type MCPState string

const (
	MCPNone         MCPState = "none"
	MCPReadOnlyHint MCPState = "readonly_hint"
	MCPMutating     MCPState = "mutating"
)

// RootSpec pairs a workspace RootRef with whether that root is a Git repository.
type RootSpec struct {
	Ref    projectroot.RootRef
	HasGit bool
}

// CoordinatorState captures one requested coordinator fixture and its resolved turn profile.
type CoordinatorState struct {
	ID                    string
	PackOverlayID         string
	SessionMode           api.SessionMode
	Posture               api.SessionPosture
	WorkflowState         WorkflowState
	RequestedSurfaceID    string
	RootTopology          RootTopology
	Loading               LoadingMode
	WebSearch             bool
	Vision                bool
	CommandExternalHidden bool
	ApprovalPosture       gate.Posture
	TempState             TempState
	MCPState              MCPState

	Roots               []RootSpec
	RootCount           int
	HasGit              bool
	HasAnyGitRoot       bool
	SessionScratchDir   string
	SharedTempLocations []string
	SpawnAllowlist      []string

	TurnProfile    surface.TurnProfile
	Plan           toolsurface.Plan
	Loaded         map[string]bool
	Overlay        PackOverlay
	Excluded       bool
	ExcludedReason string
}

type coordinatorSeed struct {
	overlayIdx int
	mode       api.SessionMode
	posture    api.SessionPosture
	wf         WorkflowState
	surfaceID  string
	roots      RootTopology
	loading    LoadingMode
	web        bool
	vision     bool
	extHidden  bool
	approval   gate.Posture
	temp       TempState
	mcp        MCPState
}

// CoordinatorStates generates the deterministic union-of-products coordinator matrix,
// recording both realizable cells and inadmissible cells with production exclusion reasons.
func CoordinatorStates(ctx context.Context) ([]CoordinatorState, error) {
	overlays, err := DiscoverPackOverlays(ctx)
	if err != nil {
		return nil, err
	}
	postures, err := Postures(overlays[0].Catalog)
	if err != nil {
		return nil, err
	}
	surfaces, err := CoordinatorSurfaces()
	if err != nil {
		return nil, err
	}
	folderless, err := FolderlessSurfaces()
	if err != nil {
		return nil, err
	}
	surfMap := make(map[string]surfacecatalog.Surface, len(surfaces))
	for _, s := range surfaces {
		surfMap[s.ID] = s.Surface
	}

	seeds := buildCoordinatorSeeds(overlays, postures, surfaces)
	planCache := make(map[string]toolsurface.Plan, 64)
	seen := make(map[string]bool, len(seeds))
	out := make([]CoordinatorState, 0, len(seeds))
	for _, seed := range seeds {
		st, buildErr := resolveCoordinatorSeed(seed, overlays[seed.overlayIdx], surfMap, folderless, planCache)
		if buildErr != nil {
			return nil, buildErr
		}
		if seen[st.ID] {
			continue
		}
		seen[st.ID] = true
		out = append(out, st)
	}
	return out, nil
}

func buildCoordinatorSeeds(
	overlays []PackOverlay,
	postures []sessionprofiles.PostureSpec,
	surfaces []SurfaceInfo,
) []coordinatorSeed {
	modes := []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk}
	rootsAll := []RootTopology{RootsNone, RootsSingleGit, RootsSingleNonGit, RootsDualGitFirst, RootsDualNonGitFirst}
	loadings := []LoadingMode{LoadingFloorOnly, LoadingPartlyLoaded, LoadingAllLoaded}

	var seeds []coordinatorSeed
	for _, mode := range modes {
		for _, p := range postures {
			for _, wf := range []WorkflowState{WorkflowAmbientIdle, WorkflowWorkersInFlight, WorkflowPendingOverlays} {
				for _, rt := range rootsAll {
					for _, ld := range loadings {
						seeds = append(seeds, coordinatorSeed{
							mode: mode, posture: p.ID, wf: wf, roots: rt, loading: ld,
							web: true, approval: gate.PostureBalanced, temp: TempScratchOnly, mcp: MCPNone,
						})
					}
				}
			}
			for _, s := range surfaces {
				for _, rt := range []RootTopology{RootsNone, RootsSingleGit, RootsDualNonGitFirst} {
					for _, ld := range loadings {
						seeds = append(seeds, coordinatorSeed{
							mode: mode, posture: p.ID, wf: WorkflowActivePhase, surfaceID: s.ID,
							roots: rt, loading: ld, web: true, approval: gate.PostureBalanced, temp: TempScratchOnly, mcp: MCPNone,
						})
					}
				}
			}
		}
	}
	seeds = append(seeds, buildOverlayAndEnvSeeds(overlays, postures, modes)...)
	return seeds
}

func buildOverlayAndEnvSeeds(
	overlays []PackOverlay,
	postures []sessionprofiles.PostureSpec,
	modes []api.SessionMode,
) []coordinatorSeed {
	var seeds []coordinatorSeed
	for ovIdx := range overlays {
		for _, mode := range modes {
			for _, p := range postures {
				for _, rt := range []RootTopology{RootsNone, RootsSingleGit} {
					for _, web := range []bool{false, true} {
						for _, vis := range []bool{false, true} {
							seeds = append(seeds, coordinatorSeed{
								overlayIdx: ovIdx, mode: mode, posture: p.ID, wf: WorkflowAmbientIdle,
								roots: rt, loading: LoadingAllLoaded, web: web, vision: vis,
								approval: gate.PostureBalanced, temp: TempScratchOnly, mcp: MCPNone,
							})
						}
					}
				}
			}
		}
	}
	for _, mode := range modes {
		for _, p := range []api.SessionPosture{api.SessionPostureBuild, api.SessionPostureSpec} {
			for _, extH := range []bool{false, true} {
				for _, app := range []gate.Posture{gate.PostureBalanced, gate.PostureStrict} {
					for _, tmp := range []TempState{TempNone, TempScratchOnly, TempScratchAndShared} {
						for _, mcp := range []MCPState{MCPNone, MCPReadOnlyHint, MCPMutating} {
							seeds = append(seeds, coordinatorSeed{
								mode: mode, posture: p, wf: WorkflowAmbientIdle, roots: RootsSingleGit,
								loading: LoadingFloorOnly, web: true, extHidden: extH,
								approval: app, temp: tmp, mcp: mcp,
							})
						}
					}
				}
			}
		}
	}
	return seeds
}

func resolveCoordinatorSeed(
	seed coordinatorSeed,
	overlay PackOverlay,
	surfMap map[string]surfacecatalog.Surface,
	folderless []string,
	planCache map[string]toolsurface.Plan,
) (CoordinatorState, error) {
	roots, hasGit, anyGit := buildRootsForTopology(seed.roots)
	scratch, shared := buildTempPaths(seed.temp)
	allowlist := slices.Clone(overlay.SpawnAllowlist)
	if !seed.web {
		allowlist = slices.DeleteFunc(allowlist, func(id string) bool { return id == "web-researcher" })
	}
	if seed.mode == api.SessionModeAsk {
		allowlist = prompts.ReadOnlyAgents(allowlist)
	}

	sess := &api.Session{Mode: seed.mode, Posture: seed.posture}
	if len(roots) > 0 {
		sess.WorkspacePath = roots[0].Ref.Path
	}
	runCtx, implState := coordinatorRunInputs(seed, allowlist)
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Inspect current state"}}
	tp := surface.ResolveTurnProfile(runCtx, sess, history, implState)

	excludedReason := coordinatorExclusionReason(seed, tp, surfMap, folderless)
	planKey := fmt.Sprintf("%s|%s|%t|%s|%s|%d", tp.SurfaceID, tp.SessionMode, tp.WorkflowActive, tp.PhaseCoordinatorSurface, tp.AdvanceWhenGateMet, len(roots))
	plan, cached := planCache[planKey]
	if !cached {
		var err error
		plan, err = surface.CompileToolPlan(tp, len(roots))
		if err != nil {
			return CoordinatorState{}, err
		}
		planCache[planKey] = plan
	}
	loaded, loadExclude := selectLoadedTools(plan, seed.loading)
	if excludedReason == "" && loadExclude != "" {
		excludedReason = loadExclude
	}

	id := fmt.Sprintf(
		"coord/%s/mode=%s/posture=%s/wf=%s/req=%s/surf=%s/roots=%s/load=%s/web=%t/vis=%t/extH=%t/app=%s/temp=%s/mcp=%s",
		overlay.ID, seed.mode, seed.posture, seed.wf, seed.surfaceID, tp.SurfaceID,
		seed.roots, seed.loading, seed.web, seed.vision, seed.extHidden, seed.approval, seed.temp, seed.mcp,
	)
	return CoordinatorState{
		ID: id, PackOverlayID: overlay.ID, SessionMode: seed.mode, Posture: seed.posture,
		WorkflowState: seed.wf, RequestedSurfaceID: seed.surfaceID, RootTopology: seed.roots,
		Loading: seed.loading, WebSearch: seed.web, Vision: seed.vision,
		CommandExternalHidden: seed.extHidden, ApprovalPosture: seed.approval,
		TempState: seed.temp, MCPState: seed.mcp, Roots: roots, RootCount: len(roots),
		HasGit: hasGit, HasAnyGitRoot: anyGit, SessionScratchDir: scratch,
		SharedTempLocations: shared, SpawnAllowlist: allowlist, TurnProfile: tp,
		Plan: plan, Loaded: loaded, Overlay: overlay,
		Excluded: excludedReason != "", ExcludedReason: excludedReason,
	}, nil
}

func coordinatorRunInputs(seed coordinatorSeed, allowlist []string) (api.CoordinatorRunContext, surface.ImplementSessionState) {
	runCtx := api.CoordinatorRunContext{AllowedAgents: allowlist}
	var st surface.ImplementSessionState
	switch seed.wf {
	case WorkflowWorkersInFlight:
		st.WorkersInFlight = 2
	case WorkflowPendingOverlays:
		st.PendingOverlayIDs = []string{"overlay-1"}
		st.WorkersDelivered = 1
	case WorkflowActivePhase:
		runCtx.RunID = "run-phase-1"
		runCtx.WorkflowID = "fixture-workflow"
		runCtx.PhaseCoordinatorSurface = seed.surfaceID
		if seed.surfaceID == "await_host" {
			runCtx.PhaseHostHeld = true
		}
		if seed.surfaceID == "workflow_compose" {
			runCtx.HasComposeDraft = true
		}
	}
	return runCtx, st
}

func coordinatorExclusionReason(
	seed coordinatorSeed,
	tp surface.TurnProfile,
	surfMap map[string]surfacecatalog.Surface,
	folderless []string,
) string {
	if seed.wf == WorkflowActivePhase && seed.surfaceID != "" {
		reqRow := surfMap[seed.surfaceID]
		if seed.mode == api.SessionModeAsk && reqRow.RequiresAgent {
			return "requires_agent_surface_rejected_in_ask_mode"
		}
		if seed.roots == RootsNone && !slices.Contains(folderless, seed.surfaceID) {
			return "workflow_phase_surface_requires_project_root"
		}
	}
	if seed.wf == WorkflowPendingOverlays && seed.roots == RootsNone {
		return "pending_overlays_require_project_root"
	}
	if seed.wf == WorkflowPendingOverlays && seed.mode == api.SessionModeAsk {
		return "pending_overlays_require_agent_mode"
	}
	if seed.roots == RootsNone && tp.SurfaceID != "implement_park" && !slices.Contains(folderless, tp.SurfaceID) {
		return "resolved_surface_requires_project_root"
	}
	return ""
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
