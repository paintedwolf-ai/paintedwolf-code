package transcript

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Pending effects wait for the original result when the program ends first.
type pendingProgramEffects struct {
	mu   sync.Mutex
	held map[string]map[string]tools.ProgramEffects
}

func (m *Service) AppendProgramEffects(ctx context.Context, sessionID, toolCallID string, late tools.ProgramEffects) {
	if m == nil || m.store == nil || (len(late.FileEdits) == 0 && late.Omitted == 0 && late.Observation == nil) {
		return
	}
	// The lookup and the hold are one step, so a row stored meanwhile finds the hold.
	m.programEffects.mu.Lock()
	messageID, err := m.store.ToolResultMessageID(ctx, sessionID, toolCallID)
	if errors.Is(err, store.ErrMessageNotFound) {
		m.programEffects.hold(sessionID, toolCallID, late)
	}
	m.programEffects.mu.Unlock()
	if err == nil {
		err = m.applyProgramEffects(ctx, sessionID, messageID, late)
	}
	if err != nil && !errors.Is(err, store.ErrMessageNotFound) {
		slog.WarnContext(ctx, "program effects not recorded", "session_id", sessionID, "tool_call_id", toolCallID, "error", err)
	}
}

func (p *pendingProgramEffects) hold(sessionID, toolCallID string, late tools.ProgramEffects) {
	if p.held == nil {
		p.held = make(map[string]map[string]tools.ProgramEffects)
	}
	if p.held[sessionID] == nil {
		p.held[sessionID] = make(map[string]tools.ProgramEffects)
	}
	p.held[sessionID][toolCallID] = late
}

// A person's next message releases effects for a result that never arrived.
func (m *Service) applyHeldProgramEffects(ctx context.Context, sessionID string, stored []api.Message) {
	type ready struct {
		messageID string
		late      tools.ProgramEffects
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
		if err := m.applyProgramEffects(ctx, sessionID, item.messageID, item.late); err != nil {
			slog.WarnContext(ctx, "held program effects not recorded", "session_id", sessionID, "message_id", item.messageID, "error", err)
		}
	}
}

func (m *Service) applyProgramEffects(ctx context.Context, sessionID, messageID string, late tools.ProgramEffects) error {
	message, err := m.store.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	if message.ToolResult == nil {
		return nil
	}
	result := *message.ToolResult
	result.FileEdits = append([]api.FileEditSnapshot(nil), result.FileEdits...)
	result.FileEditsOmitted += late.Omitted
	for _, edit := range late.FileEdits {
		var held bool
		if result.FileEdits, held = api.RecordFileEdit(result.FileEdits, edit); !held {
			result.FileEditsOmitted++
		}
	}
	if late.Observation != nil {
		facts := slices.DeleteFunc(slices.Clone(result.CardFacts), func(fact api.ActionFact) bool {
			return fact.Kind == api.ActionFactSourceObservation
		})
		result.CardFacts = append(facts, *late.Observation)
	}
	message.ToolResult = &result
	return m.Update(ctx, sessionID, messageID, message)
}
