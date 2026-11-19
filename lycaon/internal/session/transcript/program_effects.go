package transcript

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

type lateProgramEffects struct {
	edits         []api.FileEditSnapshot
	omitted       int
	sourceContext *api.SourceContext
}

// programEffects holds effects until their tool result is stored.
type programEffects struct {
	mu   sync.Mutex
	held map[string]map[string]lateProgramEffects
}

// AppendProgramEffects publishes late effects on the result that started the program.
func (m *Service) AppendProgramEffects(ctx context.Context, sessionID, toolCallID string, edits []api.FileEditSnapshot, omitted int, sourceContext *api.SourceContext) {
	if m == nil || m.store == nil || (len(edits) == 0 && omitted == 0 && sourceContext == nil) {
		return
	}
	late := lateProgramEffects{edits: edits, omitted: omitted, sourceContext: sourceContext}
	// The lookup and the hold are one step, so a row stored meanwhile finds the hold.
	m.programEffects.mu.Lock()
	messageID, err := m.store.ToolResultMessageID(ctx, sessionID, toolCallID)
	if errors.Is(err, store.ErrMessageNotFound) {
		m.programEffects.hold(sessionID, toolCallID, late)
	}
	m.programEffects.mu.Unlock()
	if err == nil {
		err = m.appendProgramEffects(ctx, sessionID, messageID, late)
	}
	if err != nil && !errors.Is(err, store.ErrMessageNotFound) {
		slog.WarnContext(ctx, "program effects not recorded", "session_id", sessionID, "tool_call_id", toolCallID, "error", err)
	}
}

func (p *programEffects) hold(sessionID, toolCallID string, late lateProgramEffects) {
	if p.held == nil {
		p.held = make(map[string]map[string]lateProgramEffects)
	}
	if p.held[sessionID] == nil {
		p.held[sessionID] = make(map[string]lateProgramEffects)
	}
	p.held[sessionID][toolCallID] = late
}

// A person's message releases effects whose result was never stored.
func (m *Service) applyHeldProgramEffects(ctx context.Context, sessionID string, stored []api.Message) {
	type ready struct {
		messageID string
		late      lateProgramEffects
	}
	var apply []ready
	m.programEffects.mu.Lock()
	if held := m.programEffects.held[sessionID]; len(held) > 0 {
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
			delete(m.programEffects.held, sessionID)
		}
	}
	m.programEffects.mu.Unlock()
	for _, item := range apply {
		if err := m.appendProgramEffects(ctx, sessionID, item.messageID, item.late); err != nil {
			slog.WarnContext(ctx, "held program effects not recorded", "session_id", sessionID, "message_id", item.messageID, "error", err)
		}
	}
}

func (m *Service) appendProgramEffects(ctx context.Context, sessionID, messageID string, late lateProgramEffects) error {
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
	if late.sourceContext != nil {
		merged := sourceref.Merge(message.SourceContext, late.sourceContext)
		if message.SourceContext != nil {
			merged.View = message.SourceContext.View
		}
		message.SourceContext = merged
	}
	return m.Update(ctx, sessionID, messageID, message)
}
