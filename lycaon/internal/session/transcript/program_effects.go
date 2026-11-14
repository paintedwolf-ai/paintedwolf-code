package transcript

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// lateEdits are file edits a program made after its call returned.
type lateEdits struct {
	edits   []api.FileEditSnapshot
	omitted int
}

// programEdits holds late edits whose tool result is not stored yet. A program
// can end between its call returning and that call's result being stored.
type programEdits struct {
	mu   sync.Mutex
	held map[string]map[string]lateEdits
}

// AppendProgramEffects adds a detached program's later file edits to the
// stored result of the call that started it, and publishes the changed row.
func (m *Service) AppendProgramEffects(ctx context.Context, sessionID, toolCallID string, edits []api.FileEditSnapshot, omitted int) {
	if m == nil || m.store == nil || (len(edits) == 0 && omitted == 0) {
		return
	}
	late := lateEdits{edits: edits, omitted: omitted}
	// The lookup and the hold are one step, so a row stored meanwhile finds the hold.
	m.programEdits.mu.Lock()
	messageID, err := m.store.ToolResultMessageID(ctx, sessionID, toolCallID)
	if errors.Is(err, store.ErrMessageNotFound) {
		m.programEdits.hold(sessionID, toolCallID, late)
	}
	m.programEdits.mu.Unlock()
	if err == nil {
		err = m.appendFileEdits(ctx, sessionID, messageID, late)
	}
	if err != nil && !errors.Is(err, store.ErrMessageNotFound) {
		slog.WarnContext(ctx, "program file edits not recorded", "session_id", sessionID, "tool_call_id", toolCallID, "error", err)
	}
}

func (p *programEdits) hold(sessionID, toolCallID string, late lateEdits) {
	if p.held == nil {
		p.held = make(map[string]map[string]lateEdits)
	}
	if p.held[sessionID] == nil {
		p.held[sessionID] = make(map[string]lateEdits)
	}
	p.held[sessionID][toolCallID] = late
}

// applyHeldProgramEdits runs after rows are stored: a stored tool result takes
// the edits held for its call, and a person's message ends the wait for a
// result that was never stored.
func (m *Service) applyHeldProgramEdits(ctx context.Context, sessionID string, stored []api.Message) {
	type ready struct {
		messageID string
		late      lateEdits
	}
	var apply []ready
	m.programEdits.mu.Lock()
	if held := m.programEdits.held[sessionID]; len(held) > 0 {
		for _, msg := range stored {
			if msg.Role == api.MessageRoleUser && msg.Visibility != api.MessageVisibilityInternal {
				clear(held)
				break
			}
			if msg.ToolResult == nil {
				continue
			}
			if late, ok := held[msg.ToolResult.ToolCallID]; ok {
				delete(held, msg.ToolResult.ToolCallID)
				apply = append(apply, ready{messageID: msg.ID, late: late})
			}
		}
		if len(held) == 0 {
			delete(m.programEdits.held, sessionID)
		}
	}
	m.programEdits.mu.Unlock()
	for _, item := range apply {
		if err := m.appendFileEdits(ctx, sessionID, item.messageID, item.late); err != nil {
			slog.WarnContext(ctx, "held program file edits not recorded", "session_id", sessionID, "message_id", item.messageID, "error", err)
		}
	}
}

func (m *Service) appendFileEdits(ctx context.Context, sessionID, messageID string, late lateEdits) error {
	message, err := m.store.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	if message.ToolResult == nil {
		return nil
	}
	result := *message.ToolResult
	result.FileEdits = append([]api.FileEditSnapshot(nil), result.FileEdits...)
	result.FileEditsOmitted += late.omitted
	for _, edit := range late.edits {
		var held bool
		if result.FileEdits, held = api.RecordFileEdit(result.FileEdits, edit); !held {
			result.FileEditsOmitted++
		}
	}
	message.ToolResult = &result
	return m.Update(ctx, sessionID, messageID, message)
}
