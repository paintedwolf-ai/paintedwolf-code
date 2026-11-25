package hoststate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

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
	RootsDualNeitherGit  RootTopology = "dual_neither_git"
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

	RunContext     api.CoordinatorRunContext
	ImplementState surface.ImplementSessionState
	History        []api.Message
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
	binding    *PhaseBinding
}

var (
	coordStatesMu   sync.Mutex
	coordStatesMemo []CoordinatorState
)

// CoordinatorStates generates the deterministic union-of-products coordinator matrix,
// recording both realizable cells and inadmissible cells with production exclusion reasons.
func CoordinatorStates(ctx context.Context) ([]CoordinatorState, error) {
	coordStatesMu.Lock()
	if coordStatesMemo != nil {
		cached := slices.Clone(coordStatesMemo)
		coordStatesMu.Unlock()
		return cached, nil
	}
	coordStatesMu.Unlock()

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

	bindings, err := ShippedPhaseBindings()
	if err != nil {
		return nil, err
	}
	seeds := buildCoordinatorSeeds(overlays, postures, surfaces)
	seeds = append(seeds, buildPhaseBindingSeeds(postures, bindings)...)
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
	coordStatesMu.Lock()
	coordStatesMemo = slices.Clone(out)
	coordStatesMu.Unlock()
	return out, nil
}

func buildCoordinatorSeeds(
	overlays []PackOverlay,
	postures []sessionprofiles.PostureSpec,
	surfaces []SurfaceInfo,
) []coordinatorSeed {
	modes := api.AllSessionModeValues()
	rootsAll := []RootTopology{
		RootsNone, RootsSingleGit, RootsSingleNonGit,
		RootsDualGitFirst, RootsDualNonGitFirst, RootsDualNeitherGit,
	}
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

// buildPhaseBindingSeeds adds an active phase for every shipped workflow
// binding, so bound surface templates, mode refs and advance policies render.
func buildPhaseBindingSeeds(postures []sessionprofiles.PostureSpec, bindings []PhaseBinding) []coordinatorSeed {
	var seeds []coordinatorSeed
	for i := range bindings {
		b := &bindings[i]
		for _, mode := range api.AllSessionModeValues() {
			for _, p := range postures {
				for _, rt := range []RootTopology{RootsNone, RootsSingleGit, RootsDualNonGitFirst} {
					for _, ld := range []LoadingMode{LoadingFloorOnly, LoadingAllLoaded} {
						seeds = append(seeds, coordinatorSeed{
							mode: mode, posture: p.ID, wf: WorkflowActivePhase, surfaceID: b.Resolved.CoordinatorSurface,
							roots: rt, loading: ld, web: true, approval: gate.PostureBalanced, temp: TempScratchOnly, mcp: MCPNone,
							binding: b,
						})
					}
				}
			}
		}
	}
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
	allApprovals := gate.AllPostures()
	for _, mode := range modes {
		for _, p := range []api.SessionPosture{api.SessionPostureBuild, api.SessionPostureSpec} {
			for _, extH := range []bool{false, true} {
				for _, app := range allApprovals {
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
		webAgents := map[string]bool{}
		for _, w := range overlay.WorkerAgents {
			if w.ToolProfile == "web_research" {
				webAgents[w.ID] = true
			}
		}
		allowlist = slices.DeleteFunc(allowlist, func(id string) bool { return webAgents[id] })
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

	excludedReason := coordinatorExclusionReason(seed, overlay, tp, surfMap, folderless)
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
	if seed.binding != nil {
		id += "/bind=" + seed.binding.WorkflowID + ":" + seed.binding.PhaseID
	}
	return CoordinatorState{
		ID: id, PackOverlayID: overlay.ID, SessionMode: seed.mode, Posture: seed.posture,
		WorkflowState: seed.wf, RequestedSurfaceID: seed.surfaceID, RootTopology: seed.roots,
		Loading: seed.loading, WebSearch: seed.web, Vision: seed.vision,
		CommandExternalHidden: seed.extHidden, ApprovalPosture: seed.approval,
		TempState: seed.temp, MCPState: seed.mcp, Roots: roots, RootCount: len(roots),
		HasGit: hasGit, HasAnyGitRoot: anyGit, SessionScratchDir: scratch,
		SharedTempLocations: shared, SpawnAllowlist: allowlist,
		RunContext: runCtx, ImplementState: implState, History: history,
		TurnProfile: tp, Plan: plan, Loaded: loaded, Overlay: overlay,
		Excluded: excludedReason != "", ExcludedReason: excludedReason,
	}, nil
}

func coordinatorRunInputs(seed coordinatorSeed, allowlist []string) (api.CoordinatorRunContext, surface.ImplementSessionState) {
	runCtx := api.CoordinatorRunContext{AllowedAgents: allowlist}
	var st surface.ImplementSessionState
	switch seed.wf {
	case WorkflowAmbientIdle:
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
		if seed.binding != nil {
			applyPhaseBinding(&runCtx, *seed.binding)
		}
	}
	return runCtx, st
}

// RunStateVariants returns st under each active run state, its turn profile
// re-resolved by the production selector. Run-state fields do not enter tool
// projection, so the variants share st's authority.
func RunStateVariants(st CoordinatorState) ([]CoordinatorState, error) {
	if strings.TrimSpace(st.RunContext.RunID) == "" {
		return nil, nil
	}
	sess := &api.Session{Mode: st.SessionMode, Posture: st.Posture}
	if len(st.Roots) > 0 {
		sess.WorkspacePath = st.Roots[0].Ref.Path
	}
	contexts := ActiveRunStateContexts(st.RunContext)
	out := make([]CoordinatorState, 0, len(contexts))
	for _, runCtx := range contexts {
		v := st
		v.RunContext = runCtx
		v.TurnProfile = surface.ResolveTurnProfile(runCtx, sess, st.History, st.ImplementState)
		if v.TurnProfile.SurfaceID != st.TurnProfile.SurfaceID {
			return nil, fmt.Errorf("%s: run state %q resolved surface %q, want %q", st.ID,
				surface.ResolveRunStateOverlay(runCtx), v.TurnProfile.SurfaceID, st.TurnProfile.SurfaceID)
		}
		v.ID = st.ID + "/run=" + string(surface.ResolveRunStateOverlay(runCtx))
		out = append(out, v)
	}
	return out, nil
}

func surfaceTemplatesLoadedInCatalog(overlay PackOverlay, tp surface.TurnProfile) bool {
	if overlay.Catalog == nil {
		return true
	}
	if tp.SurfaceTemplate != "" {
		if _, _, ok := prompts.ResolveCatalogTemplateUnit(overlay.Catalog, tp.SurfaceTemplate); !ok {
			return false
		}
	}
	for _, modeRef := range tp.ModeRefs {
		if _, _, ok := prompts.ResolveCatalogTemplateUnit(overlay.Catalog, surface.ModeTemplateRef(modeRef)); !ok {
			return false
		}
	}
	return true
}

func coordinatorExclusionReason(
	seed coordinatorSeed,
	overlay PackOverlay,
	tp surface.TurnProfile,
	surfMap map[string]surfacecatalog.Surface,
	folderless []string,
) string {
	if !surfaceTemplatesLoadedInCatalog(overlay, tp) {
		return "surface_template_pack_removed_in_overlay"
	}
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
