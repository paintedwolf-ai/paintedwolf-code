package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// ResolvedWorkflowRequest binds request text to its workflow start.
type ResolvedWorkflowRequest struct {
	RunID            string
	OpeningMessageID string
	Text             string
}

// resolvedWorkflowRequest supplies a coordinator's resolved workflow request.
func (m *Manager) resolvedWorkflowRequest(ctx context.Context, sess *api.Session) ResolvedWorkflowRequest {
	if m == nil || m.workflows == nil || sess == nil || sess.IsWorkerChild() {
		return ResolvedWorkflowRequest{}
	}
	return m.workflows.ResolvedRequest(ctx, sess.ID)
}

// turnRequest names the human request a turn decides for. A turn that carries
// its own instruction decides for it. A turn a host opened decides only when
// the request it continues has no decision yet: a workflow started from a
// slash command, or a user turn the host parked before any model call.
func (m *Manager) turnRequest(ctx context.Context, in PromptInput, history []api.Message, openingMessageID string, resolvedRequest ResolvedWorkflowRequest) (string, string, bool) {
	if in.HostSignal == nil {
		if text := promptUserInstruction(in); text != "" {
			if strings.TrimSpace(openingMessageID) == "" {
				openingMessageID = newestUserMessageID(history)
			}
			return openingMessageID, text, true
		}
	}
	opening, text := requestOpening(history, resolvedRequest)
	if opening == "" || text == "" || m.requestDecided(ctx, opening) {
		return "", "", false
	}
	return opening, text, true
}

func requestOpening(history []api.Message, request ResolvedWorkflowRequest) (string, string) {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if request.OpeningMessageID != "" && msg.ID == request.OpeningMessageID && request.Text != "" {
			return msg.ID, request.Text
		}
		if request.RunID != "" && msg.WorkflowRunID != request.RunID {
			continue
		}
		if isUserInstruction(msg) {
			return msg.ID, strings.TrimSpace(api.MessageUserInstructionContent(msg))
		}
	}
	return request.OpeningMessageID, request.Text
}

func isUserInstruction(msg api.Message) bool {
	return msg.Role == api.MessageRoleUser && msg.Origin == api.MessageOriginUser && strings.TrimSpace(msg.ID) != "" && strings.TrimSpace(api.MessageUserInstructionContent(msg)) != ""
}

// newestUserInstruction is the last message a person wrote.
func newestUserInstruction(history []api.Message) (api.Message, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if isUserInstruction(msg) {
			return msg, true
		}
	}
	return api.Message{}, false
}

// requestDecided reports whether a turn decision already serves the request
// the message opened.
func (m *Manager) requestDecided(ctx context.Context, openingMessageID string) bool {
	if m == nil || m.store == nil {
		return true
	}
	receipts, err := m.store.ListTurnLoadReceiptsForTurns(ctx, []string{openingMessageID})
	if err != nil {
		// An unreadable ledger keeps the standing surface rather than re-deciding it.
		turnLoadLog.Warn("turn load receipts unreadable", "opening_message_id", openingMessageID, "error", err)
		return true
	}
	for _, receipt := range receipts {
		if receipt.Trigger == store.TurnLoadTriggerTurn {
			return true
		}
	}
	return false
}

// beginUndecidedTurn preserves applicable guidance when no task can be resolved.
func (m *Manager) beginUndecidedTurn(ctx context.Context, sess *api.Session, history []api.Message, surfaceID, profileID, openingMessageID string) {
	if strings.TrimSpace(openingMessageID) == "" {
		openingMessageID = newestUserMessageID(history)
	}
	_, rootCount, _ := m.workspaceRootsForPrompt(ctx, sess)
	_, loadable, _ := m.turnToolSets(ctx, sess, profileID, surfaceID, rootCount)
	_, recorded := m.recordedStanding(ctx, sess.ID)
	m.turnLoads.BeginUndecidedTurn(sess.ID, turnload.TurnOpening{
		OpeningMessageID: openingMessageID,
		Loadable:         loadable,
		Boundary:         m.turnBoundary(ctx, sess, recorded),
		HistoryEpoch:     m.historyEpoch(ctx, sess),
	})
}
