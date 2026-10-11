package execution

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/pkg/api"
)

const sessionRecoveryPageSize = 256

// RecoverOrphanedTurns idles sessions left busy by process death. Still busy at
// boot means the engine went away under a live turn, so they settle as
// interrupted.
func (m *Recovery) RecoverOrphanedTurns(ctx context.Context) error {
	if m == nil || m.store == nil {
		return nil
	}
	if err := m.transcript.RepairPendingOutputs(ctx); err != nil {
		return err
	}
	if _, err := m.store.RecoverTurns(ctx); err != nil {
		return err
	}
	if _, err := m.store.SettleAbandonedTurnClocks(ctx); err != nil {
		return err
	}
	for {
		sessionIDs, err := m.store.ListBusySessionIDs(ctx, sessionRecoveryPageSize)
		if err != nil {
			return err
		}
		for _, sessionID := range sessionIDs {
			if err := m.store.SetSessionStatus(ctx, sessionID, api.SessionStatusIdle); err != nil {
				return err
			}
			m.status.PublishIdle(ctx, sessionID, api.SessionIdleDispositionInterrupted)
		}
		if len(sessionIDs) < sessionRecoveryPageSize {
			return nil
		}
	}
}

// RecoverOrphanedTurnsForSession repairs projections and fences one session after a panic.
func (m *Recovery) RecoverOrphanedTurnsForSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || m.store == nil || sessionID == "" {
		return nil
	}
	// Global repair visits only unacknowledged projections.
	if err := m.transcript.RepairPendingOutputs(ctx); err != nil {
		return err
	}
	if _, err := m.store.RecoverTurnsForSession(ctx, sessionID); err != nil {
		return err
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.Status != api.SessionStatusBusy {
		return nil
	}
	if err := m.store.SetSessionStatus(ctx, sessionID, api.SessionStatusIdle); err != nil {
		return err
	}
	m.status.PublishIdle(ctx, sessionID, api.SessionIdleDispositionInterrupted)
	return nil
}

// RecoverInterruptedToolResults appends a tool-result for each interrupted
// invocation that has no matching transcript row.
func (m *Recovery) RecoverInterruptedToolResults(ctx context.Context) error {
	if m == nil || m.store == nil || m.invocations == nil {
		return nil
	}
	lister, ok := m.invocations.(invocation.ProjectionRecoveryRecorder)
	if !ok {
		return fmt.Errorf("invocation recorder does not support bounded projection recovery")
	}
	return m.recoverInterruptedProjectionPages(ctx, lister.ListInterruptedWithoutResult)
}

func (m *Recovery) RecoverInterruptedToolResultPagesForSession(ctx context.Context, lister invocation.SessionProjectionRecoveryRecorder, sessionID string) error {
	return m.recoverInterruptedProjectionPages(ctx, func(ctx context.Context) ([]invocation.InterruptedProjection, error) {
		return lister.ListInterruptedWithoutResultForSession(ctx, sessionID)
	})
}

func (m *Recovery) recoverInterruptedProjectionPages(ctx context.Context, list func(context.Context) ([]invocation.InterruptedProjection, error)) error {
	for {
		pending, err := list(ctx)
		if err != nil {
			return err
		}
		bySession := make(map[string][]api.Message)
		order := make([]string, 0)
		for _, item := range pending {
			if _, seen := bySession[item.SessionID]; !seen {
				order = append(order, item.SessionID)
			}
			bySession[item.SessionID] = append(bySession[item.SessionID], interruptedToolMessage(item))
		}
		for _, sessionID := range order {
			if err := m.transcript.AppendPlain(ctx, sessionID, bySession[sessionID]...); err != nil {
				return err
			}
		}
		if len(pending) < sessionRecoveryPageSize {
			return nil
		}
	}
}

func interruptedToolMessage(item invocation.InterruptedProjection) api.Message {
	rec := item.Receipt
	content := "The host stopped while this tool was in flight."
	facts := guidance.ToolResultFacts{
		Outcome: api.ToolResultOutcomeError,
		Codes:   []string{"TOOL_OWNER_INTERRUPTED"},
	}
	toolResult := guidance.ComposeToolResult(content, facts, nil)
	if toolResult == nil {
		toolResult = &api.ToolResult{Content: content, Outcome: api.ToolResultOutcomeError}
	}
	toolResult.Tool = rec.Tool
	toolResult.ToolCallID = rec.ToolCallID
	toolResult.AssistantMessageID = item.AssistantMessageID
	toolResult.Invocation = &rec
	return api.Message{
		ID:         uuid.NewString(),
		Role:       api.MessageRoleTool,
		Content:    content,
		ToolResult: toolResult,
		CreatedAt:  time.Now().UTC(),
	}
}
