package hoststate

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

// FreezeWorkerAuthority computes the production tool list, schema projections,
// native program reach, and Reader for a WorkerState through toolpolicy.Engine
// and promptloop.ProjectTurnTools.
func FreezeWorkerAuthority(ctx context.Context, state WorkerState) (TurnAuthority, error) {
	overlay := state.Overlay
	if overlay.Catalog == nil {
		overlays, err := DiscoverPackOverlays(ctx)
		if err != nil {
			return TurnAuthority{}, err
		}
		overlay = overlays[0]
	}
	askChild := state.ParentSessionMode == api.SessionModeAsk
	bundle, narrowed, err := executorBundleForOverlay(
		overlay, state.Posture, askChild, state.WebSearch, state.CommandExternalHidden, MCPNone,
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
	projTurnMu.Lock()
	proj, hit := projTurnCache[cacheKey]
	projTurnMu.Unlock()
	if !hit {
		workerSess := &api.Session{
			ID:              "worker-fixture",
			ParentSessionID: "coord-fixture",
			Mode:            state.ParentSessionMode,
			Posture:         state.Posture,
			AgentType:       state.AgentID,
		}
		if state.RootCount > 0 {
			workerSess.WorkspacePath = "/tmp/fixture-primary"
		}
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
		offeredMetas, plan, _, pErr := promptloop.ProjectTurnTools(ctx, deps, workerSess, nil, frame)
		if pErr != nil {
			return TurnAuthority{}, fmt.Errorf("worker ProjectTurnTools: %w", pErr)
		}
		filter := platform.ToolFilter{ProfileID: state.ToolProfile, ProjectRootCount: state.RootCount}
		progReach, rErr := bundle.programReach(ctx, filter)
		if rErr != nil {
			return TurnAuthority{}, fmt.Errorf("worker Metadata.ProgramReach: %w", rErr)
		}
		progTable, tErr := bundle.programTable(ctx, filter)
		if tErr != nil {
			return TurnAuthority{}, fmt.Errorf("worker Metadata.ProgramTable: %w", tErr)
		}
		postLoadSchemas, dErr := projectDeferredSchemas(ctx, deps, workerSess, nil, frame, plan, state.Loaded, offeredMetas)
		if dErr != nil {
			return TurnAuthority{}, fmt.Errorf("worker projectDeferredSchemas: %w", dErr)
		}
		offered := toolMetaNames(offeredMetas)
		projectedSchemas := toolMetaSchemas(offeredMetas)
		encSchemas, encErr := encodeAdmittedValue(reflect.ValueOf(projectedSchemas), map[ptrVisit]bool{})
		proj = projectedTurnToolsEntry{
			metas:            offeredMetas,
			plan:             plan,
			progReach:        progReach,
			progTable:        progTable,
			postLoadSchemas:  postLoadSchemas,
			offered:          offered,
			projectedSchemas: projectedSchemas,
			encodedSchemas:   encSchemas,
			schemasOK:        encErr == nil,
			programs:         computeProgramsList(progReach),
		}
		projTurnMu.Lock()
		projTurnCache[cacheKey] = proj
		projTurnMu.Unlock()
	}

	visible := slices.Clone(proj.offered)
	visible = append(visible, proj.plan.DeferredNames()...)
	var reviewsConfinedWrites bool
	if state.ApprovalPosture != "" {
		reviewsConfinedWrites = state.ApprovalPosture.ReviewsConfinedWrites()
	}
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             state.ToolProfile,
		SessionMode:           string(state.ParentSessionMode),
		Profiles:              narrowed,
		Plan:                  proj.plan,
		Loaded:                state.Loaded,
		Visible:               visible,
		Schemas:               overlay.Schemas,
		ProjectedSchemas:      proj.projectedSchemas,
		RootCount:             state.RootCount,
		HasGit:                state.HasGit,
		HasAnyGitRoot:         state.HasAnyGitRoot,
		OnWorkerBranch:        state.OnWorkerBranch,
		WebSearch:             state.WebSearch,
		ReviewsConfinedWrites: reviewsConfinedWrites,
		CommandExternalHidden: state.CommandExternalHidden,
	})
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("worker NewReader: %w", err)
	}
	reader = reader.WithVision(state.Vision)
	return assembleTurnAuthorityFromEntry(proj, reader), nil
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
	if sessionState.HasGit {
		bv = BranchMainGit
		mutCapable, _ := prompts.AgentMutationCapable(target.ID)
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
		RootCount:             max(sessionState.RootCount, 1),
		Loading:               WorkerLoadingStickyOnly,
		ParentSessionMode:     sessionState.SessionMode,
		WebSearch:             sessionState.WebSearch,
		Vision:                sessionState.Vision,
		CommandExternalHidden: sessionState.CommandExternalHidden,
		ApprovalPosture:       sessionState.ApprovalPosture,
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
