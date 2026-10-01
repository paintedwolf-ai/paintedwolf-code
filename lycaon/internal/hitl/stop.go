package hitl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// CancelPendingForSession cancels pending checkpoints for one session.
func (m *Manager) CancelPendingForSession(ctx context.Context, sessionID, reason string) error {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session_id required")
	}
	rows, err := m.store.ListBySession(ctx, sessionID, DecisionStatusPending, nil)
	if err != nil {
		return err
	}
	var cancelErr error
	for i := range rows {
		cancelErr = errors.Join(cancelErr, m.cancelPending(ctx, rows[i].ID, reason))
	}
	return cancelErr
}

func (m *Manager) cancelPending(ctx context.Context, checkpointID, reason string) error {
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if row.Status != DecisionStatusPending {
		return nil
	}
	resolution := stopResolution(ctx)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "user stopped the session"
		if resolution.By == authzledger.ResolvedByHostStop {
			reason = "the session stopped"
		}
	}
	result := DecisionResult{Approved: false, Comments: reason}
	now := time.Now().UTC()
	viaOutbox, err := m.store.resolveCheckpoint(
		ctx,
		*row,
		DecisionStatusCanceled,
		&result,
		nil,
		now,
		resolution,
		m.resolutionSeal(ctx, DecisionStatusCanceled),
	)
	if err != nil {
		return err
	}
	row.Status = DecisionStatusCanceled
	row.Result = &result
	row.ResolvedAt = &now
	row.Resolution = &resolution
	m.announceResolved(ctx, *row, viaOutbox)
	if row.Kind == api.CheckpointKindToolApproval {
		m.notifyToolApprovalTerminal(*row, DecisionStatusCanceled)
	}
	return nil
}
