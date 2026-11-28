package hoststate

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/readerfacts"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

// FreezeWorkerAuthority computes the production tool list, schema projections,
// native program reach, and Reader for a WorkerState through toolpolicy.Engine
// and promptloop.ProjectTurnTools.
func FreezeWorkerAuthority(ctx context.Context, state WorkerState) (TurnAuthority, error) {
	store, err := heldAuthorityMemos()
	if err != nil {
		return TurnAuthority{}, err
	}
	overlay := state.Overlay
	if overlay.Catalog == nil {
		overlays, err := DiscoverPackOverlays(ctx)
		if err != nil {
			return TurnAuthority{}, err
		}
		overlay = overlays[0]
	}
	askChild := state.ParentSessionMode == api.SessionModeAsk
	bundle, narrowed, err := executorBundleForOverlayWithStore(
		store, overlay, state.Posture, askChild, state.WebSearch, state.CommandExternalHidden, MCPNone,
	)
	if err != nil {
		return TurnAuthority{}, err
	}

	cacheKey := fmt.Sprintf(
		"w|%s|%s|%s|%s|%s|%d|%t|%t|%s",
		overlay.Catalog.Revision, state.ParentSessionMode, state.Posture,
		state.AgentID, state.ToolProfile, state.RootCount,
		state.WebSearch, state.CommandExternalHidden, sortedBoolKeys(state.Loaded),
	)
	fullKey := fmt.Sprintf(
		"%s|bv=%s|sec=%t|git=%t/%t|on=%t|appr=%s|scratch=%s|temp=%+v|vision=%t",
		cacheKey, state.BranchView, state.SecondaryRootGit, state.HasGit, state.HasAnyGitRoot,
		state.OnWorkerBranch, state.ApprovalPosture, state.SessionScratchDir, state.SharedTempLocations, state.Vision,
	)
	store.frozenMu.RLock()
	if cachedAuth, ok := store.frozenWorkers[fullKey]; ok {
		store.frozenMu.RUnlock()
		return cachedAuth, nil
	}
	store.frozenMu.RUnlock()

	store.projTurnMu.Lock()
	proj, hit := store.projTurns[cacheKey]
	store.projTurnMu.Unlock()
	if !hit {
		workerSess := workerFixtureSession(state)
		policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
			ToolLister:       bundle.exec.Metadata,
			ProjectRootCount: func(context.Context, *api.Session) int { return state.RootCount },
		})
		deps := promptloop.ContextDeps{
			Tools:            bundle.reg,
			Policy:           policy,
			LoadedTools:      func(string) map[string]bool { return maps.Clone(state.Loaded) },
			ProjectRootCount: func(context.Context, *api.Session) int { return state.RootCount },
			ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
				WebSearchEnabled: func() bool { return state.WebSearch },
			},
		}
		frame := inject.CoordinatorTurnFrame{
			Machine: inject.Machine{ProfileID: state.ToolProfile, SkillCount: 1},
		}
		turn, pErr := promptloop.ProjectTurnTools(ctx, deps, workerSess, nil, frame)
		if pErr != nil {
			return TurnAuthority{}, fmt.Errorf("worker ProjectTurnTools: %w", pErr)
		}
		offeredMetas, plan := turn.Metas, turn.Plan
		filter := platform.ToolFilter{ProfileID: state.ToolProfile, ProjectRootCount: state.RootCount}
		progReach, rErr := bundle.programReach(ctx, filter)
		if rErr != nil {
			return TurnAuthority{}, fmt.Errorf("worker Metadata.ProgramReach: %w", rErr)
		}
		progTable, tErr := bundle.programTable(ctx, filter)
		if tErr != nil {
			return TurnAuthority{}, fmt.Errorf("worker Metadata.ProgramTable: %w", tErr)
		}
		postLoadSchemas := turn.DeferredSchemas
		offered := toolMetaNames(offeredMetas)
		projectedSchemas := toolMetaSchemas(offeredMetas)
		schemasDigest, encErr := digestAdmittedValue(projectedSchemas)
		proj = projectedTurnToolsEntry{
			metas:            offeredMetas,
			plan:             plan,
			progReach:        progReach,
			progTable:        progTable,
			postLoadSchemas:  postLoadSchemas,
			offered:          offered,
			projectedSchemas: projectedSchemas,
			schemasDigest:    schemasDigest,
			schemasOK:        encErr == nil,
			programs:         computeProgramsList(progReach),
		}
		store.projTurnMu.Lock()
		store.projTurns[cacheKey] = proj
		store.projTurnMu.Unlock()
	}

	host, err := WorkerHostFacts(state, narrowed, overlay.Schemas)
	if err != nil {
		return TurnAuthority{}, err
	}
	reader, err := prompts.NewReader(host, prompts.TurnProjection{
		ProfileID:        state.ToolProfile,
		Plan:             proj.plan,
		Offered:          proj.offered,
		ProjectedSchemas: mergedSchemas(proj.postLoadSchemas, proj.projectedSchemas),
	})
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("worker NewReader: %w", err)
	}
	reader = reader.WithVision(state.Vision)
	assembled := assembleTurnAuthorityFromEntry(proj, reader)
	store.frozenMu.Lock()
	if len(store.frozenWorkers) < 1024 {
		store.frozenWorkers[fullKey] = assembled
	}
	store.frozenMu.Unlock()
	return assembled, nil
}

// worker fixture paths; the branch checkout is a Git repository.
const (
	workerFixturePrimary   = "/tmp/fixture-primary"
	workerFixtureSecondary = "/tmp/fixture-secondary"
	workerFixtureBranch    = "/tmp/fixture-worker-branch"
)

func workerFixtureSession(state WorkerState) *api.Session {
	sess := &api.Session{
		ID:              "worker-fixture",
		ParentSessionID: "coord-fixture",
		Mode:            state.ParentSessionMode,
		Posture:         state.Posture,
		AgentType:       state.AgentID,
	}
	if state.RootCount > 0 {
		sess.WorkspacePath = workerFixturePrimary
	}
	return sess
}

// WorkerHostFacts derives state's host facts through the production
// derivation from the roots and branch checkout its branch view describes.
func WorkerHostFacts(state WorkerState, profiles []sandbox.ToolProfile, schemas *toolschema.Config) (prompts.HostFacts, error) {
	repos := map[string]bool{}
	var roots []projectroot.RootRef
	if state.RootCount > 0 {
		roots = append(roots, projectroot.RootRef{ID: "r1", Label: "primary", Path: workerFixturePrimary, IsPrimary: true})
		repos[workerFixturePrimary] = state.BranchView != BranchMainNonGit
	}
	for i := 2; i <= state.RootCount; i++ {
		path := fmt.Sprintf("%s-%d", workerFixtureSecondary, i)
		roots = append(roots, projectroot.RootRef{ID: fmt.Sprintf("r%d", i), Label: "secondary", Path: path})
		repos[path] = state.SecondaryRootGit
	}
	obs := readerfacts.Observation{
		Session:               workerFixtureSession(state),
		Profiles:              profiles,
		Schemas:               schemas,
		Roots:                 roots,
		WebSearch:             state.WebSearch,
		ApprovalPosture:       state.ApprovalPosture,
		CommandExternalHidden: state.CommandExternalHidden,
		SessionScratchDir:     state.SessionScratchDir,
		SharedTempLocations:   state.SharedTempLocations,
		IsRepo:                func(path string) bool { return repos[path] },
	}
	if state.BranchView == BranchIsolatedWorker {
		obs.BranchRoot, obs.OnWorkerBranch = workerFixtureBranch, true
		repos[workerFixtureBranch] = true
	}
	host, err := readerfacts.Derive(obs)
	if err != nil {
		return prompts.HostFacts{}, err
	}
	if host.HasGit != state.HasGit || host.HasAnyGitRoot != (state.HasGit || state.HasAnyGitRoot) || host.OnWorkerBranch != state.OnWorkerBranch {
		return prompts.HostFacts{}, fmt.Errorf("state %s: derived git=%t/%t branch=%t disagree with generated git=%t/%t branch=%t",
			state.ID, host.HasGit, host.HasAnyGitRoot, host.OnWorkerBranch, state.HasGit, state.HasAnyGitRoot, state.OnWorkerBranch)
	}
	return host, nil
}

// WorkerReaderForSession builds the prompt Reader for a named worker agent
// spawned from sessionState, respecting posture and Ask-mode read-only clamping.
func WorkerReaderForSession(ctx context.Context, sessionState CoordinatorState, agentID string) (prompts.Reader, error) {
	overlay := sessionState.Overlay
	if overlay.Catalog == nil {
		overlays, err := DiscoverPackOverlays(ctx)
		if err != nil {
			return prompts.Reader{}, err
		}
		overlay = overlays[0]
	}
	var target *WorkerAgent
	for i := range overlay.WorkerAgents {
		if overlay.WorkerAgents[i].ID == agentID {
			target = &overlay.WorkerAgents[i]
			break
		}
	}
	if target == nil {
		return prompts.Reader{}, fmt.Errorf("worker agent %q not found in overlay %q", agentID, overlay.ID)
	}
	bv := BranchMainNonGit
	mutCapable := profileMutationCapable(overlay, target.ToolProfile)
	if sessionState.HasGit {
		bv = BranchMainGit
		if mutCapable && sessionState.SessionMode != api.SessionModeAsk {
			bv = BranchIsolatedWorker
		}
	}
	ws := WorkerState{
		AgentID:               target.ID,
		ToolProfile:           target.ToolProfile,
		ExecMode:              target.ExecMode,
		Posture:               sessionState.Posture,
		BranchView:            bv,
		RootCount:             sessionState.RootCount,
		Loading:               WorkerLoadingStickyOnly,
		ParentSessionMode:     sessionState.SessionMode,
		WebSearch:             sessionState.WebSearch,
		Vision:                sessionState.Vision,
		CommandExternalHidden: sessionState.CommandExternalHidden,
		ApprovalPosture:       sessionState.ApprovalPosture,
		MutationCapable:       mutCapable,
		HasGit:                sessionState.HasGit,
		HasAnyGitRoot:         sessionState.HasAnyGitRoot,
		OnWorkerBranch:        bv == BranchIsolatedWorker,
		Loaded:                map[string]bool{},
		Overlay:               overlay,
	}
	auth, err := FreezeWorkerAuthority(ctx, ws)
	if err != nil {
		return prompts.Reader{}, err
	}
	return auth.Reader, nil
}

func admissibleWorkerDeferredNames(
	ctx context.Context,
	overlay PackOverlay,
	agentID, profileID string,
	posture api.SessionPosture,
	parentMode api.SessionMode,
	rootCount int,
	webSearch, extHidden bool,
) ([]string, error) {
	probe := WorkerState{
		AgentID:               agentID,
		ToolProfile:           profileID,
		Posture:               posture,
		BranchView:            BranchMainGit,
		RootCount:             max(rootCount, 1),
		Loading:               WorkerLoadingStickyOnly,
		ParentSessionMode:     parentMode,
		WebSearch:             webSearch,
		CommandExternalHidden: extHidden,
		HasGit:                true,
		HasAnyGitRoot:         true,
		Loaded:                map[string]bool{},
		Overlay:               overlay,
	}
	auth, err := FreezeWorkerAuthority(ctx, probe)
	if err != nil {
		return nil, err
	}
	deferred := slices.Clone(auth.Plan.DeferredNames())
	slices.Sort(deferred)
	return deferred, nil
}
