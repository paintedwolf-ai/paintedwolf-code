package assembly

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// appendPreHistorySystemInjects adds the session's standing blocks before history.
func (e *AssemblyEngine) appendPreHistorySystemInjects(
	ctx context.Context,
	sess *api.Session,
	turn *TurnAssemblyScratch,
	out []api.Message,
	lastStableIdx int,
) ([]api.Message, int) {
	if injectMsgs := e.agentsMDIndexInject(ctx, sess, turn); len(injectMsgs) > 0 {
		out = append(out, injectMsgs...)
		lastStableIdx = len(out) - 1
	}
	return out, lastStableIdx
}

// interleaveSourceBriefs places each turn's source-change brief directly
// before the prompt that opened the turn, where its window ends. Briefs are
// fixed at turn open, so history stays append-only.
func (e *AssemblyEngine) interleaveSourceBriefs(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch, messages []api.Message) []api.Message {
	blocks := e.sourceBriefBlocks(ctx, sess, turn)
	if len(blocks) == 0 {
		return messages
	}
	out := make([]api.Message, 0, len(messages)+len(blocks))
	for _, m := range messages {
		if block, ok := blocks[m.ID]; ok {
			out = append(out, api.Message{
				ID:   "host-source-changes-" + m.ID,
				Role: api.MessageRoleSystem, Content: block,
				Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
			})
		}
		out = append(out, m)
	}
	return out
}

// sourceBriefBlocks renders the session's turn briefs once per prompt run.
func (e *AssemblyEngine) sourceBriefBlocks(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) map[string]string {
	if turn != nil && turn.SourceBriefsLoaded {
		return turn.SourceBriefBlocks
	}
	deps := e.deps()
	var blocks map[string]string
	if deps.TurnSourceBriefs != nil {
		for openingID, brief := range deps.TurnSourceBriefs(ctx, sess) {
			if block := inject.RenderSourceChangesBlock(ctx, deps.Injects, sess.ID, brief); block != "" {
				if blocks == nil {
					blocks = map[string]string{}
				}
				blocks[openingID] = block
			}
		}
	}
	if turn != nil {
		turn.SourceBriefBlocks, turn.SourceBriefsLoaded = blocks, true
	}
	return blocks
}

// buildTailSystemInjects returns dynamic blocks and the final coordinator board.
func (e *AssemblyEngine) buildTailSystemInjects(
	ctx context.Context,
	sess *api.Session,
	coordinator bool,
	frame inject.CoordinatorTurnFrame,
	pendingKickIDs []string,
	history []api.Message,
	turn *TurnAssemblyScratch,
) ([]api.Message, error) {
	var tail []api.Message

	tail = append(tail, e.agentsMDChainInject(ctx, sess, history, turn)...)
	if block, ok := e.skillProcedureInject(ctx, sess, frame.Machine.ProfileID); ok {
		tail = append(tail, block)
	} else if block, ok := e.skillPointerInject(ctx, sess, frame.Machine.ProfileID); ok {
		tail = append(tail, block)
	}

	if coordinator {
		if block, ok := e.prependTransitionInject(ctx, sess, frame, history, turn); ok {
			tail = append(tail, api.Message{Role: api.MessageRoleSystem, Content: block})
		}
	}

	deps := e.deps()
	if deps.CommandJobs != nil {
		jobs := deps.CommandJobs(sess.ID)
		var held []heldcall.Running
		if deps.HeldCalls != nil {
			held = deps.HeldCalls(sess.ID)
		}
		surface := "worker"
		if coordinator {
			surface = "coordinator"
		}
		if block := inject.RenderCommandJobsBlock(ctx, deps.Injects, sess.ID, jobs, held, time.Now().UTC(), surface); block != "" {
			tail = append(tail, api.Message{
				Role: api.MessageRoleSystem, Content: block,
				Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
			})
		}
	}

	if coordinator && !sess.IsWorkerChild() {
		surfaceID := ""
		if turn != nil {
			surfaceID = turn.SurfaceID
		}
		if injectMsgs := e.prependSynthesisEvidenceInject(ctx, sess, surfaceID); len(injectMsgs) > 0 {
			tail = append(tail, injectMsgs...)
		}
	}

	if coordinator && deps.CoordinatorFrame != nil && deps.Injects != nil {
		injectMsgs, err := e.prependCoordinatorRunInject(ctx, sess, frame, pendingKickIDs, turn, history)
		if err != nil {
			return tail, err
		}
		tail = append(tail, injectMsgs...)
	}

	if sess.IsWorkerChild() && deps.Board != nil {
		if block, ok := e.workerBoard(ctx, sess, turn); ok {
			tail = append(tail, api.Message{Role: api.MessageRoleSystem, Content: block})
		}
	}

	return tail, nil
}

// boardOrientRefreshLimit bounds frame refreshes after orientation advances a run.
const boardOrientRefreshLimit = 3

// prepareCoordinatorBoard refreshes state after board-driven advancement.
func (e *AssemblyEngine) prepareCoordinatorBoard(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
) (inject.CoordinatorTurnFrame, string, error) {
	deps := e.deps()
	if deps.Board == nil {
		return frame, "", nil
	}
	if scoped, ok := deps.Board.(interface {
		WithRepositoryFacts(context.Context) context.Context
	}); ok {
		ctx = scoped.WithRepositoryFacts(ctx)
	}
	run := frame.RunContext
	block, changed := deps.Board.PrependBoardIfChanged(ctx, sess, run)
	// The board marks each run and phase oriented once, so every inject it
	// renders is recorded; a refresh onto another run or phase renders a new one.
	for pass := 0; changed && deps.BoardOrientReady != nil; pass++ {
		if hash := deps.Board.BoardInjectHash(sess.ID); hash != "" {
			if err := deps.BoardOrientReady.RecordBoardOrientReady(ctx, sess.ID, hash); err != nil {
				deps.Board.InvalidateOrientation(sess.ID)
				return frame, "", err
			}
		}
		if deps.CoordinatorFrame == nil || pass == boardOrientRefreshLimit {
			break
		}
		refreshed, err := deps.CoordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess)
		if err != nil {
			deps.Board.InvalidateOrientation(sess.ID)
			return frame, "", err
		}
		machine := frame.Machine
		frame = refreshed
		inject.StampMachine(&frame, machine)
		if frame.RunContext.RunID == run.RunID && frame.RunContext.CurrentPhase == run.CurrentPhase {
			break
		}
		run = frame.RunContext
		var refreshedBlock string
		refreshedBlock, changed = deps.Board.PrependBoardIfChanged(ctx, sess, run)
		if changed {
			block = refreshedBlock
		}
	}
	if strings.TrimSpace(block) == "" && turn.Iteration > 0 {
		block = turn.BoardBlock
	}
	if strings.TrimSpace(block) != "" {
		hash := deps.Board.BoardInjectHash(sess.ID)
		turn.BoardBlock = block
		turn.BoardKey = boardInjectFingerprint(hash, strings.TrimSpace(frame.RunContext.CurrentPhase))
	}
	return frame, block, nil
}

func (e *AssemblyEngine) prependCoordinatorRunInject(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	pendingKickIDs []string,
	turn *TurnAssemblyScratch,
	history []api.Message,
) ([]api.Message, error) {
	deps := e.deps()
	var out []api.Message
	runCtx := frame.RunContext
	if frame.Roster == nil {
		frame.Roster = e.resolveTurnRoster(ctx, sess, frame, history, turn)
	}

	// Workflow state is request-local and must accompany every bound request.
	if strings.TrimSpace(runCtx.WorkflowID) != "" {
		hintCodes := surface.StaticWorkflowHintCodes(runCtx, runCtx.HasComposeDraft)
		hintCodes = anchor.FilterSuppressedHintCodes(hintCodes, runCtx, pendingKickIDs...)
		snap := frame.Runtime
		// The binding selects the template stem.
		gateFeedback := deps.GateFeedback
		if deps.Workflows != nil {
			if manifest, ok := deps.Workflows.ActiveManifest(ctx, sess.ID); ok {
				gateFeedback = gateFeedback.WithWorkflowArchive(manifest.Archive)
			}
		}
		block, err := inject.RenderActiveWorkflowInject(ctx, deps.Injects, sess.ID, frame, deps.WorkflowHints, hintCodes, gateFeedback)
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
		if view := e.sessionCatalogView(ctx, sess); view != nil {
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
		if strings.TrimSpace(block) != "" && e.taskOffered(sess, facts.SurfaceID, facts.RootCount) {
			out = append(out, api.Message{Role: api.MessageRoleSystem, Content: block})
		}
	}
	return out, nil
}

func (e *AssemblyEngine) prependTransitionInject(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) (string, bool) {
	if e == nil || e.deps().Prompts == nil || sess == nil || turn == nil {
		return "", false
	}
	profile, implState := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	previous := e.loadExecutionModeState(ctx, sess.ID).LastFamily
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

	inj := e.deps().Injects
	if inj == nil {
		return "", false
	}
	surfaceVars := map[string]any{"project_dir": sess.WorkspacePath}
	if roots, count, activePath, ok := e.workspaceRootsForTurn(ctx, sess, turn); ok {
		surfaceVars["root_count"] = count
		surfaceVars["workspace_roots"] = roots
		if activePath != "" {
			surfaceVars["project_dir"] = activePath
		}
	}
	var toolProfiles []sandbox.ToolProfile
	turnSurface := prompts.SurfaceTurn{Loaded: e.loadedTools(sess)}
	if view := e.sessionCatalogView(ctx, sess); view != nil {
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
