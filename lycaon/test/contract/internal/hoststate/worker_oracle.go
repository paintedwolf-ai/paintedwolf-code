package hoststate

import (
	"context"
	"fmt"
	"maps"

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
		Machine: inject.Machine{ProfileID: state.ToolProfile},
	}
	offeredMetas, plan, _, err := promptloop.ProjectTurnTools(ctx, deps, workerSess, nil, frame)
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("worker ProjectTurnTools: %w", err)
	}

	filter := platform.ToolFilter{ProfileID: state.ToolProfile, ProjectRootCount: state.RootCount}
	progReach, err := bundle.exec.Metadata.ProgramReach(ctx, filter)
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("worker Metadata.ProgramReach: %w", err)
	}
	offered := toolMetaNames(offeredMetas)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:      state.ToolProfile,
		Profiles:       narrowed,
		Plan:           plan,
		Loaded:         state.Loaded,
		Visible:        offered,
		Schemas:        overlay.Schemas,
		RootCount:      state.RootCount,
		HasGit:         state.HasGit,
		OnWorkerBranch: state.OnWorkerBranch,
		WebSearch:      state.WebSearch,
	})
	if err != nil {
		return TurnAuthority{}, fmt.Errorf("worker NewReader: %w", err)
	}
	reader = reader.WithVision(state.Vision)
	return assembleTurnAuthority(offeredMetas, offered, plan, progReach, reader), nil
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
