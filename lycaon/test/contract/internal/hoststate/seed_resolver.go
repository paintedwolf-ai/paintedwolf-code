package hoststate

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// SeedDomains bundles the immutable, sorted primitive state domains discovered
// from the effective catalog for direct single-state seed resolution (without
// indexing into prebuilt matrix rows).
type SeedDomains struct {
	Overlays         []PackOverlay
	Postures         []sessionprofiles.PostureSpec
	Surfaces         []SurfaceInfo
	SurfaceMap       map[string]surfacecatalog.Surface
	Folderless       []string
	SessionModes     []api.SessionMode
	WorkflowStates   []WorkflowState
	RootTopologies   []RootTopology
	LoadingModes     []LoadingMode
	ApprovalPostures []gate.Posture
	TempStates       []TempState
	MCPStates        []MCPState
	WorkerBranches   []WorkerBranchView
	WorkerLoadings   []WorkerLoadingMode
}

// CoordinatorSeed specifies primitive coordinates for resolving a single CoordinatorState.
type CoordinatorSeed struct {
	OverlayIndex          int
	SessionMode           api.SessionMode
	Posture               api.SessionPosture
	WorkflowState         WorkflowState
	SurfaceID             string
	RootTopology          RootTopology
	Loading               LoadingMode
	WebSearch             bool
	Vision                bool
	CommandExternalHidden bool
	ApprovalPosture       gate.Posture
	TempState             TempState
	MCPState              MCPState
}

// WorkerSeed specifies primitive coordinates for resolving a single WorkerState.
type WorkerSeed struct {
	OverlayIndex             int
	AgentID                  string
	Posture                  api.SessionPosture
	BranchView               WorkerBranchView
	RootCount                int
	Loading                  WorkerLoadingMode
	ParentSessionMode        api.SessionMode
	ParentUpgradedAskToAgent bool
	WebSearch                bool
	Vision                   bool
	CommandExternalHidden    bool
	ApprovalPosture          gate.Posture
}

var (
	loadSeedDomainsOnce = sync.OnceValues(func() (*SeedDomains, error) {
		ctx := context.Background()
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
		return &SeedDomains{
			Overlays:         overlays,
			Postures:         postures,
			Surfaces:         surfaces,
			SurfaceMap:       surfMap,
			Folderless:       folderless,
			SessionModes:     []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk},
			WorkflowStates:   []WorkflowState{WorkflowActivePhase, WorkflowAmbientIdle, WorkflowPendingOverlays, WorkflowWorkersInFlight},
			RootTopologies:   []RootTopology{RootsDualGitFirst, RootsDualNonGitFirst, RootsNone, RootsSingleGit, RootsSingleNonGit},
			LoadingModes:     []LoadingMode{LoadingAllLoaded, LoadingFloorOnly, LoadingPartlyLoaded},
			ApprovalPostures: []gate.Posture{gate.PostureBalanced, gate.PostureStrict},
			TempStates:       []TempState{TempNone, TempScratchAndShared, TempScratchOnly},
			MCPStates:        []MCPState{MCPMutating, MCPNone, MCPReadOnlyHint},
			WorkerBranches:   []WorkerBranchView{BranchIsolatedWorker, BranchLinkedWorktreeGit, BranchMainGit, BranchMainNonGit},
			WorkerLoadings:   []WorkerLoadingMode{WorkerLoadingAllLoaded, WorkerLoadingStickyOnly},
		}, nil
	})
)

// DefaultSeedDomains returns the cached, sorted catalog-backed primitive domains.
func DefaultSeedDomains() (*SeedDomains, error) {
	return loadSeedDomainsOnce()
}

// ResolveCoordinatorSeed resolves one CoordinatorSeed directly through the
// production surface, plan, and exclusion rules without enumerating the matrix.
func ResolveCoordinatorSeed(domains *SeedDomains, seed CoordinatorSeed) (CoordinatorState, error) {
	if domains == nil || len(domains.Overlays) == 0 {
		return CoordinatorState{}, fmt.Errorf("seed domains required")
	}
	ovIdx := seed.OverlayIndex
	if ovIdx < 0 || ovIdx >= len(domains.Overlays) {
		ovIdx = 0
	}
	internal := coordinatorSeed{
		overlayIdx: ovIdx,
		mode:       seed.SessionMode,
		posture:    seed.Posture,
		wf:         seed.WorkflowState,
		surfaceID:  seed.SurfaceID,
		roots:      seed.RootTopology,
		loading:    seed.Loading,
		web:        seed.WebSearch,
		vision:     seed.Vision,
		extHidden:  seed.CommandExternalHidden,
		approval:   seed.ApprovalPosture,
		temp:       seed.TempState,
		mcp:        seed.MCPState,
	}
	return resolveCoordinatorSeed(
		internal,
		domains.Overlays[ovIdx],
		domains.SurfaceMap,
		domains.Folderless,
		make(map[string]toolsurface.Plan, 4),
	)
}

// ResolveWorkerSeed resolves one WorkerSeed directly through the catalog's
// worker profiles and production exclusion rules without enumerating the matrix.
func ResolveWorkerSeed(domains *SeedDomains, seed WorkerSeed) (WorkerState, error) {
	if domains == nil || len(domains.Overlays) == 0 {
		return WorkerState{}, fmt.Errorf("seed domains required")
	}
	ovIdx := seed.OverlayIndex
	if ovIdx < 0 || ovIdx >= len(domains.Overlays) {
		ovIdx = 0
	}
	overlay := domains.Overlays[ovIdx]
	var target *WorkerAgent
	for i := range overlay.WorkerAgents {
		if overlay.WorkerAgents[i].ID == seed.AgentID {
			target = &overlay.WorkerAgents[i]
			break
		}
	}
	if target == nil {
		return WorkerState{
			ID:             fmt.Sprintf("worker/%s/%s/missing", overlay.ID, seed.AgentID),
			AgentID:        seed.AgentID,
			Overlay:        overlay,
			Excluded:       true,
			ExcludedReason: "worker_agent_absent_in_overlay",
		}, nil
	}
	var prof sandbox.ToolProfile
	for _, p := range overlay.Profiles {
		if p.ID == target.ToolProfile {
			prof = p
			break
		}
	}
	parentAsk := seed.ParentSessionMode == api.SessionModeAsk
	upgraded := seed.ParentUpgradedAskToAgent && parentAsk
	rootCount := seed.RootCount
	if rootCount <= 0 {
		rootCount = 1
	}
	st := buildWorkerState(overlay, *target, prof, seed.Posture, seed.BranchView, rootCount, seed.Loading, parentAsk, upgraded)
	st.WebSearch = seed.WebSearch
	st.Vision = seed.Vision
	st.CommandExternalHidden = seed.CommandExternalHidden
	if seed.ApprovalPosture != "" {
		st.ApprovalPosture = seed.ApprovalPosture
	}
	st.ID = fmt.Sprintf(
		"%s/ov=%s/web=%t/vis=%t/extH=%t/app=%s",
		st.ID, overlay.ID, st.WebSearch, st.Vision, st.CommandExternalHidden, st.ApprovalPosture,
	)
	return st, nil
}
