package hitl

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// resolveContentApply validates a decision against the stored host plan,
// composes final bytes, and commits them before the blocked writer is notified.
func (m *Checkpoints) resolveContentApply(ctx context.Context, sessionID, checkpointID string, requested *ContentApplyResolve) (*CheckpointResponse, error) {
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()

	row, err := m.Store.Get(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if row.SessionID != sessionID {
		return nil, ErrCheckpointNotFound
	}
	if row.Kind != api.CheckpointKindContentApply {
		return nil, ErrCheckpointKindMismatch
	}
	if row.Status != DecisionStatusPending {
		if contentApplyReplayMatches(row, requested) {
			return storedToCheckpointResponse(row), nil
		}
		return nil, ErrCheckpointNotPending
	}
	if requested == nil {
		return nil, ErrContentApplySelectionInvalid
	}

	resolved := &ContentApplyResolve{
		Decision: requested.Decision, ApprovedHunks: append([]string(nil), requested.ApprovedHunks...),
		Guidance: requested.Guidance,
	}
	status := DecisionStatusApproved
	switch requested.Decision {
	case api.ContentApplyReject:
		if len(requested.ApprovedHunks) != 0 {
			return nil, ErrContentApplySelectionInvalid
		}
		status = DecisionStatusRejected
	case api.ContentApplyApprove, api.ContentApplyApprovePartial:
		raw, _ := row.Payload["content_apply_plan"].(map[string]any)
		plan, planErr := contentApplyPlanFromMap(raw)
		if planErr != nil {
			return nil, fmt.Errorf("content_apply plan: %w", planErr)
		}
		resolved.FinalAfter, err = plan.Compose(requested.Decision, requested.ApprovedHunks)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrContentApplySelectionInvalid
	}

	resolution, err := personResolution(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	viaOutbox, err := m.Store.resolveCheckpoint(ctx, *row, status, nil, resolved, now, resolution,
		m.Authority.resolutionSeal(ctx, status))
	if err != nil {
		return nil, err
	}
	row.Status = status
	row.ContentResult = resolved
	row.ResolvedAt = &now
	row.Resolution = &resolution
	m.announceResolved(ctx, *row, viaOutbox)
	return storedToCheckpointResponse(row), nil
}

func contentApplyReplayMatches(row *StoredCheckpoint, requested *ContentApplyResolve) bool {
	if row == nil || requested == nil || row.ContentResult == nil {
		return false
	}
	stored := row.ContentResult
	if stored.Decision != requested.Decision || stored.Guidance != requested.Guidance || len(stored.ApprovedHunks) != len(requested.ApprovedHunks) {
		return false
	}
	for i := range stored.ApprovedHunks {
		if stored.ApprovedHunks[i] != requested.ApprovedHunks[i] {
			return false
		}
	}
	return true
}
