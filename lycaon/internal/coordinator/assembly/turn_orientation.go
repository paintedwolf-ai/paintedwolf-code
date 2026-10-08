package assembly

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// turnContextAssembler projects volatile host state around stable prompt history.
type turnContextAssembler struct {
	surface *promptSurface
}

func (e *turnContextAssembler) prependCoordinatorRunInject(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	pendingKickIDs []string,
	turn *TurnAssemblyScratch,
	history []api.Message,
) ([]api.Message, error) {
	deps := e.surface.wiring
	var out []api.Message
	runCtx := frame.RunContext
	if frame.Roster == nil {
		frame.Roster = e.surface.resolveTurnRoster(ctx, sess, frame, history, turn)
	}

	// Workflow state is request-local and must accompany every bound request.
	if strings.TrimSpace(runCtx.WorkflowID) != "" {
		hintCodes := surface.StaticWorkflowHintCodes(runCtx, runCtx.HasComposeDraft)
		hintCodes = anchor.FilterSuppressedHintCodes(hintCodes, runCtx, pendingKickIDs...)
		snap := frame.Runtime
		// The binding selects the template stem.
		block, err := inject.RenderActiveWorkflowInject(ctx, deps.Injects, sess.ID, frame, deps.WorkflowHints, hintCodes, deps.GateFeedback)
		if err != nil {
			return nil, fmt.Errorf("active-workflow inject (%s): %w", inject.ActiveWorkflowRenderStem(ctx, sess.ID), err)
		}
		if strings.TrimSpace(block) != "" {
			out = append(out, api.Message{Role: api.MessageRoleSystem, Content: block})
		}
		if snap.Blueprint != nil {
			govBlock, err := inject.RenderBlueprintInject(ctx, deps.Injects, sess.ID, snap.Blueprint)
			if err != nil {
				return nil, fmt.Errorf("blueprint inject: %w", err)
			}
			if strings.TrimSpace(govBlock) != "" {
				out = append(out, api.Message{Role: api.MessageRoleSystem, Content: govBlock})
			}
		}
	}

	// Spawn-capable sessions always receive the active roster.
	if len(frame.Roster.Declared) > 0 {
		facts := frame.Roster.Facts
		spawnAgents := frame.Roster.Effective
		if len(spawnAgents) == 0 {
			return out, nil
		}
		var catalog *extpacks.EffectiveCatalog
		var toolProfiles []sandbox.ToolProfile
		if view := e.surface.sessionCatalogView(ctx, sess); view != nil {
			catalog = view.Catalog
			toolProfiles = view.ToolProfiles
		}
		block, err := inject.RenderImplementSpawnInject(
			ctx, deps.Injects, sess.ID, spawnAgents, spawn.MaxInFlightTaskWorkers, facts.SurfaceID, facts.RootCount, facts.RepoKnownEmpty, facts.WebSearchEnabled,
			catalog, toolProfiles,
		)
		if err != nil {
			return nil, fmt.Errorf("implement-spawn inject: %w", err)
		}
		if strings.TrimSpace(block) != "" && e.surface.taskOffered(sess, facts.SurfaceID, facts.RootCount) {
			out = append(out, api.Message{Role: api.MessageRoleSystem, Content: block})
		}
	}
	return out, nil
}
func (e *turnContextAssembler) workerBoard(
	ctx context.Context,
	sess *api.Session,
	turn *TurnAssemblyScratch,
) (string, bool) {
	if e == nil || e.surface.wiring.Board == nil {
		return "", false
	}
	block, ok := e.surface.wiring.Board.WorkerBoard(ctx, sess)
	if !ok {
		return "", false
	}
	turn.BoardBlock = block
	turn.BoardKey = e.surface.wiring.Board.BoardInjectHash(sess.ID)
	return block, true
}
func (e *turnContextAssembler) prependTransitionInject(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) (string, bool) {
	if e == nil || e.surface.wiring.Prompts == nil || sess == nil || turn == nil {
		return "", false
	}
	profile, implState := e.surface.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	previous := e.surface.loadExecutionModeState(ctx, sess.ID).LastFamily
	current := surface.ExecutionModeFamily(profile.SurfaceID)
	transition := surface.ComputeModeTransition(previous, current, turn.ModeTransitionCauses)
	if !inject.ShouldRenderTransitionInject(transition) {
		return "", false
	}
	key := inject.TransitionInjectKey(sess.ID, turn.PromptTurnSeq, transition)
	if turn.Iteration > 0 && key == turn.TransitionInjectKey && strings.TrimSpace(turn.TransitionInjectBlock) != "" {
		return turn.TransitionInjectBlock, true
	}
	if turn.Iteration > 0 && key == turn.TransitionInjectKey {
		return "", false
	}

	inj := e.surface.wiring.Injects
	if inj == nil {
		return "", false
	}
	surfaceVars := map[string]any{"project_dir": sess.WorkspacePath}
	if roots, count, activePath, ok := e.surface.workspaceRootsForTurn(ctx, sess, turn); ok {
		surfaceVars["root_count"] = count
		surfaceVars["workspace_roots"] = roots
		if activePath != "" {
			surfaceVars["project_dir"] = activePath
		}
	}
	var toolProfiles []sandbox.ToolProfile
	turnSurface := prompts.SurfaceTurn{Loaded: e.surface.loadedTools(sess)}
	if view := e.surface.sessionCatalogView(ctx, sess); view != nil {
		toolProfiles = view.ToolProfiles
		turnSurface.Schemas = view.ToolSchemas
	}
	if err := prompts.MergeCoordinatorSurfacePathVars(profile.SurfaceID, toolProfiles, surfaceVars, turnSurface); err != nil {
		return "", false
	}
	if err := prompts.MergeCoordinatorPromptVars(
		profile.SurfaceID,
		prompts.ExecutionModePromptTransition{
			ExecutionMode:         transition.ExecutionMode,
			ExecutionModePrevious: transition.ExecutionModePrevious,
			ExecutionModeEntered:  transition.ExecutionModeEntered,
			ExecutionModeLeft:     transition.ExecutionModeLeft,
		},
		prompts.CoordinatorPromptGates{
			PendingOverlayPromote: len(implState.PendingOverlayIDs) > 0,
			VerifyRequired:        frame.RequiresEvidence("verify"),
			VerifyCommand:         implState.VerifyCommand,
		},
		surfaceVars,
	); err != nil {
		return "", false
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectTransition, anchor.MatchContext{Surface: "coordinator", SessionID: sess.ID}, inj, surfaceVars)
	if err != nil || strings.TrimSpace(block) == "" {
		return "", false
	}
	turn.TransitionInjectKey = key
	turn.TransitionInjectBlock = strings.TrimSpace(block)
	return turn.TransitionInjectBlock, true
}
