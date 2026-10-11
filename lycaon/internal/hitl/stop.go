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
func (m *Checkpoints) CancelPendingForSession(ctx context.Context, sessionID, reason string) error {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session_id required")
	}
	rows, err := m.Store.ListBySession(ctx, sessionID, DecisionStatusPending, nil)
	if err != nil {
		return err
	}
	var cancelErr error
	for i := range rows {
		cancelErr = errors.Join(cancelErr, m.cancelPending(ctx, rows[i].ID, reason))
	}
	return cancelErr
}

func (m *Checkpoints) cancelPending(ctx context.Context, checkpointID, reason string) error {
	resolution := stopResolution(ctx)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "user stopped the session"
		if resolution.By == authzledger.ResolvedByHostStop {
			reason = "the session stopped"
		}
	}
	return m.settlePending(ctx, checkpointID, DecisionStatusCanceled, DecisionResult{Approved: false, Comments: reason}, resolution)
}

// settlePending settles a still-pending checkpoint the host answers itself.
func (m *Checkpoints) settlePending(ctx context.Context, checkpointID string, status DecisionStatus, result DecisionResult, resolution Resolution) error {
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	row, err := m.Store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if row.Status != DecisionStatusPending {
		return nil
	}
	now := time.Now().UTC()
	viaOutbox, err := m.Store.resolveCheckpoint(
		ctx,
		*row,
		status,
		&result,
		nil,
		now,
		resolution,
		m.Authority.resolutionSeal(ctx, status),
	)
	if err != nil {
		return err
	}
	row.Status = status
	row.Result = &result
	row.ResolvedAt = &now
	row.Resolution = &resolution
	m.announceResolved(ctx, *row, viaOutbox)
	if row.Kind == api.CheckpointKindToolApproval {
		m.Authority.notifyToolApprovalTerminal(*row, status)
	}
	return nil
}
