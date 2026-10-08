package assembly

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// appendPreHistorySystemInjects adds the session's standing blocks before history.
func (e *turnContextAssembler) appendPreHistorySystemInjects(
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
func (e *turnContextAssembler) interleaveSourceBriefs(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch, messages []api.Message) []api.Message {
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
func (e *turnContextAssembler) sourceBriefBlocks(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) map[string]string {
	if turn != nil && turn.SourceBriefsLoaded {
		return turn.SourceBriefBlocks
	}
	deps := e.deps
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
func (e *turnContextAssembler) buildTailSystemInjects(
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

	deps := e.deps
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
func (e *turnContextAssembler) prepareCoordinatorBoard(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
) (inject.CoordinatorTurnFrame, string, error) {
	deps := e.deps
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
