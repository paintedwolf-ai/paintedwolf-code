package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *turnContextAssembler) skillGuidanceReader(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, history []api.Message, turn *TurnAssemblyScratch) (prompts.Reader, bool) {
	if turn != nil && turn.Reader.Valid() {
		return turn.Reader, true
	}
	if e == nil || e.surface == nil || sess == nil {
		return prompts.Reader{}, false
	}
	if sess.IsWorkerChild() {
		r, err := e.surface.workerTurnReader(ctx, sess, frame, turn)
		return r, err == nil && r.Valid()
	}
	r, err := e.surface.turnReader(ctx, sess, frame, history, turn, 0)
	return r, err == nil && r.Valid()
}

func (e *turnContextAssembler) skillProcedureWithTool(ctx context.Context, sess *api.Session, profileID string, frame inject.CoordinatorTurnFrame, history []api.Message, turn *TurnAssemblyScratch) (api.Message, string, bool) {
	deps := e.deps
	if deps.SkillPreload == nil || deps.Injects == nil || sess == nil {
		return api.Message{}, "", false
	}
	preload := deps.SkillPreload(sess.ID)
	if preload == nil || strings.TrimSpace(preload.Body) == "" {
		return api.Message{}, "", false
	}
	reader, ok := e.skillGuidanceReader(ctx, sess, frame, history, turn)
	if !ok {
		return api.Message{}, "", false
	}
	block, err := inject.RenderSkillProcedureBlock(ctx, deps.Injects, reader, sess.ID, profileID, *preload)
	if err != nil || block == "" {
		return api.Message{}, "", false
	}
	return api.Message{ID: "host-skill-procedure", Role: api.MessageRoleSystem, Content: block,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted}, preload.Tool, true
}

// skillPointerInject names, in one line, the skill a turn's first loadable
// tool call fitted without reading it. A read skill supersedes the pointer.
func (e *turnContextAssembler) skillPointerInject(ctx context.Context, sess *api.Session, profileID string, frame inject.CoordinatorTurnFrame, history []api.Message, turn *TurnAssemblyScratch) (api.Message, bool) {
	deps := e.deps
	if deps.SkillPointer == nil || deps.Injects == nil || sess == nil {
		return api.Message{}, false
	}
	if deps.SkillPreload != nil && deps.SkillPreload(sess.ID) != nil {
		return api.Message{}, false
	}
	pointer := deps.SkillPointer(sess.ID)
	if pointer == nil {
		return api.Message{}, false
	}
	reader, ok := e.skillGuidanceReader(ctx, sess, frame, history, turn)
	if !ok {
		return api.Message{}, false
	}
	block, err := inject.RenderSkillPointerBlock(ctx, deps.Injects, reader, sess.ID, profileID, *pointer)
	if err != nil || block == "" {
		return api.Message{}, false
	}
	return api.Message{ID: "host-skill-pointer", Role: api.MessageRoleSystem, Content: block,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted}, true
}

// interleaveSkillGuidance anchors a turn's preloaded skill procedure or pointer
// in history at the turn opener or the triggering tool batch, so it remains at
// one fixed position in the cached prefix and compacts with history.
func (e *turnContextAssembler) interleaveSkillGuidance(ctx context.Context, sess *api.Session, profileID string, frame inject.CoordinatorTurnFrame, history []api.Message, turn *TurnAssemblyScratch, messages []api.Message) []api.Message {
	if len(messages) == 0 {
		return messages
	}
	openerIdx := api.UserIntentBoundary(messages) - 1
	if openerIdx < 0 {
		return messages
	}
	block, tool, ok := e.skillProcedureWithTool(ctx, sess, profileID, frame, history, turn)
	if !ok {
		block, ok = e.skillPointerInject(ctx, sess, profileID, frame, history, turn)
		if !ok {
			return messages
		}
		tool = "*"
	}
	insertAt := openerIdx + 1
	if tool == "" {
		if insertAt < len(messages) && isCompactedOrOmittedTurnBoundary(messages[insertAt]) {
			return messages
		}
	} else {
		if afterIdx := firstToolBatchEndInTurn(messages, openerIdx+1, tool); afterIdx > insertAt {
			insertAt = afterIdx
		} else if insertAt < len(messages) && isCompactedOrOmittedTurnBoundary(messages[insertAt]) {
			return messages
		}
	}
	out := make([]api.Message, 0, len(messages)+1)
	out = append(out, messages[:insertAt]...)
	out = append(out, block)
	out = append(out, messages[insertAt:]...)
	return out
}

func firstToolBatchEndInTurn(messages []api.Message, from int, tool string) int {
	for i := from; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != api.MessageRoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		matched := false
		for _, tc := range msg.ToolCalls {
			if tool == "*" || tc.Name == tool {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		end := i + 1
		for end < len(messages) && messages[end].Role == api.MessageRoleTool {
			end++
		}
		return end
	}
	return 0
}

func isCompactedOrOmittedTurnBoundary(msg api.Message) bool {
	if msg.CompactionCheckpoint || msg.ID == "host-history-coverage" || msg.ID == "host-context-trim-notice" {
		return true
	}
	return len(msg.ContentParts) == 1 && msg.ContentParts[0].Source == "compaction_summary"
}
