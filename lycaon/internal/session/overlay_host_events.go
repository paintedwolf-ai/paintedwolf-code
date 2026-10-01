package session

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetOverlayPromoter wires overlay merge operations.
func (m *Manager) SetOverlayPromoter(p OverlayPromoter) {
	if m == nil {
		return
	}
	m.overlayPromoter = p
}

// PromoteOverlay merges one overlay and records the landing.
func (m *Manager) PromoteOverlay(ctx context.Context, sessionID, overlayID string, in api.PromoteOverlayInput) (api.WorkerMergeResult, error) {
	if m == nil || m.overlayPromoter == nil || m.store == nil {
		return api.WorkerMergeResult{}, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	overlayID = strings.TrimSpace(overlayID)
	if sessionID == "" || overlayID == "" {
		return api.WorkerMergeResult{}, nil
	}
	// Attribute host-initiated promotions to the active user turn.
	if in.UserTurn == 0 {
		if turn, err := m.store.UserTurnOrdinal(ctx, sessionID); err == nil {
			in.UserTurn = turn
		}
	}
	m.reconcileCoordinatorBatchFromLedger(ctx, sessionID)
	out, err := m.overlayPromoter.PromoteOverlay(ctx, sessionID, overlayID, in)
	if err != nil {
		return out, err
	}
	defer m.maybeRunPromotion(context.WithoutCancel(ctx), sessionID)
	banners, bErr := guidance.RenderOverlayRebaseConflictBanners(ctx, overlayID, out.Rebased)
	if bErr != nil {
		return out, bErr
	}
	if err := m.appendHostEvent(ctx, sessionID, hostmarker.OverlayPromoteEventPrefix, out, banners); err != nil {
		return out, err
	}
	m.reconcileCoordinatorBatchFromLedger(ctx, sessionID)
	return out, nil
}

// RejectOverlay closes an overlay and records affected children.
func (m *Manager) RejectOverlay(ctx context.Context, sessionID, overlayID, reason string) (api.OverlayRejectOutcome, error) {
	if m == nil || m.overlayPromoter == nil || m.store == nil {
		return api.OverlayRejectOutcome{}, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	overlayID = strings.TrimSpace(overlayID)
	if sessionID == "" || overlayID == "" {
		return api.OverlayRejectOutcome{}, nil
	}
	m.reconcileCoordinatorBatchFromLedger(ctx, sessionID)
	out, err := m.overlayPromoter.RejectOverlay(ctx, sessionID, overlayID, reason)
	if err != nil {
		return out, err
	}
	defer m.maybeRunPromotion(context.WithoutCancel(ctx), sessionID)
	banners, bErr := guidance.RenderOverlayParentRejectedBanners(ctx, overlayID, out.Orphaned)
	if bErr != nil {
		return out, bErr
	}
	if err := m.appendHostEvent(ctx, sessionID, hostmarker.OverlayRejectEventPrefix, out, banners); err != nil {
		return out, err
	}
	m.reconcileCoordinatorBatchFromLedger(ctx, sessionID)
	return out, nil
}

// FormatOverlayHostToolContent renders an overlay event body.
func FormatOverlayHostToolContent(prefix string, payload any, banners string) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(prefix) + " " + string(raw)
	if strings.TrimSpace(banners) != "" {
		content += "\n" + strings.TrimSpace(banners)
	}
	return content, nil
}

func (m *Manager) appendHostEvent(ctx context.Context, sessionID, prefix string, payload any, banners string) error {
	content, err := FormatOverlayHostToolContent(prefix, payload, banners)
	if err != nil {
		return err
	}
	msg := api.Message{
		ID:      uuid.NewString(),
		Role:    api.MessageRoleTool,
		Content: content,
		ToolResult: &api.ToolResult{
			Content: content,
			Outcome: api.ToolResultOutcomeCompleted,
		},
		// An empty run ID records an event outside a workflow run.
		WorkflowRunID: m.activeWorkflowRunID(ctx, sessionID),
		CreatedAt:     time.Now().UTC(),
	}
	if promotion, ok := payload.(api.WorkerMergeResult); ok && promotion.Status == api.WorkerMergeStatusMerged {
		msg.ToolResult.OverlayPromotion = promotion.OverlayPromotion
	}
	return m.AppendAndPublishMessages(ctx, sessionID, msg)
}
