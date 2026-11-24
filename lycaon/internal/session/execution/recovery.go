package execution

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

const sessionRecoveryPageSize = 256

// RecoverOrphanedTurns idles sessions left busy by process death. Still busy at
// boot means the engine went away under a live turn, so they settle as
// interrupted.
func (m *Recovery) RecoverOrphanedTurns(ctx context.Context) error {
	if m == nil || (m.sessionStore == nil) {
		return nil
	}
	if err := m.transcript.RepairPendingOutputs(ctx); err != nil {
		return err
	}
	recovering, err := m.turnStore.RecoverTurns(ctx)
	if err != nil {
		return err
	}
	if err := m.withdrawUnsettledDrafts(ctx, recovering); err != nil {
		return err
	}
	if _, err := m.turnStore.SettleAbandonedTurnClocks(ctx); err != nil {
		return err
	}
	for {
		sessionIDs, err := m.sessionStore.ListBusySessionIDs(ctx, sessionRecoveryPageSize)
		if err != nil {
			return err
		}
		for _, sessionID := range sessionIDs {
			if err := m.sessionStore.SetSessionStatus(ctx, sessionID, api.SessionStatusIdle); err != nil {
				return err
			}
			m.status.PublishIdle(ctx, sessionID, api.SessionIdleDispositionInterrupted)
		}
		if len(sessionIDs) < sessionRecoveryPageSize {
			return nil
		}
	}
}

// withdrawUnsettledDrafts is idempotent, so a session with several
// recovering turns is simply visited again.
func (m *Recovery) withdrawUnsettledDrafts(ctx context.Context, recovering []store.Turn) error {
	for _, turn := range recovering {
		if err := m.transcript.WithdrawUnsettledDrafts(ctx, turn.SessionID); err != nil {
			return err
		}
	}
	return nil
}

// RecoverOrphanedTurnsForSession repairs projections and fences one session after a panic.
func (m *Recovery) RecoverOrphanedTurnsForSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || (m.sessionStore == nil) || sessionID == "" {
		return nil
	}
	// Global repair visits only unacknowledged projections.
	if err := m.transcript.RepairPendingOutputs(ctx); err != nil {
		return err
	}
	recovering, err := m.turnStore.RecoverTurnsForSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := m.withdrawUnsettledDrafts(ctx, recovering); err != nil {
		return err
	}
	sess, err := m.sessionStore.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.Status != api.SessionStatusBusy {
		return nil
	}
	if err := m.sessionStore.SetSessionStatus(ctx, sessionID, api.SessionStatusIdle); err != nil {
		return err
	}
	m.status.PublishIdle(ctx, sessionID, api.SessionIdleDispositionInterrupted)
	return nil
}

// RecoverSettledToolResults appends a tool result for each settled invocation
// whose result row is missing.
func (m *Recovery) RecoverSettledToolResults(ctx context.Context) error {
	if m == nil || (m.sessionStore == nil) || m.invocations == nil {
		return nil
	}
	lister, ok := m.invocations.(invocation.ProjectionRecoveryRecorder)
	if !ok {
		return fmt.Errorf("invocation recorder does not support bounded projection recovery")
	}
	return m.recoverUnansweredPages(ctx, lister.ListSettledWithoutResult)
}

// ReconcileToolResults restores missing result rows for one session before its
// history is assembled, so every recorded call reaches the model once.
func (m *Recovery) ReconcileToolResults(ctx context.Context, sessionID string) error {
	if m == nil || m.invocations == nil {
		return nil
	}
	lister, ok := m.invocations.(invocation.SessionProjectionRecoveryRecorder)
	if !ok {
		return nil
	}
	return m.recoverUnansweredPages(ctx, func(ctx context.Context) ([]invocation.UnansweredReceipt, error) {
		return lister.ListSettledWithoutResultForSession(ctx, sessionID)
	})
}

func (m *Recovery) recoverUnansweredPages(ctx context.Context, list func(context.Context) ([]invocation.UnansweredReceipt, error)) error {
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
			bySession[item.SessionID] = append(bySession[item.SessionID], unansweredToolMessage(item))
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

// unansweredToolMessage projects the receipt's recorded outcome. An interrupted
// owner left no outcome; a settled refusal keeps only its code.
func unansweredToolMessage(item invocation.UnansweredReceipt) api.Message {
	rec := item.Receipt
	var toolResult *api.ToolResult
	if rec.Status == api.InvocationStatusInterrupted {
		content := "The host stopped while this tool was in flight."
		toolResult = guidance.ComposeToolResult(content, guidance.ToolResultFacts{
			Outcome: api.ToolResultOutcomeError,
			Codes:   []string{toolrejection.ToolOwnerInterruptedCode},
		}, nil)
	} else {
		toolResult = settledRefusalResult(rec)
	}
	toolResult.Tool = rec.Tool
	toolResult.ToolCallID = rec.ToolCallID
	toolResult.AssistantMessageID = item.AssistantMessageID
	toolResult.Invocation = &rec
	return api.Message{
		ID:         uuid.NewString(),
		Role:       api.MessageRoleTool,
		Content:    toolResult.Content,
		ToolResult: toolResult,
		CreatedAt:  time.Now().UTC(),
	}
}

func settledRefusalResult(rec api.InvocationReceipt) *api.ToolResult {
	code := ""
	if rec.Failure != nil {
		code = strings.TrimSpace(rec.Failure.Code)
	}
	outcome := api.ToolResultOutcomeError
	if rec.Status == api.InvocationStatusRejected {
		outcome = api.ToolResultOutcomeRejected
	}
	content := toolrejection.RenderRejectBlock(code, nil, nil, nil).Error()
	return guidance.ComposeToolResult(content, guidance.ToolResultFacts{Outcome: outcome, Codes: []string{code}}, nil)
}
