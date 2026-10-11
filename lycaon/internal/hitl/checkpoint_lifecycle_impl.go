package hitl

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetCheckpointExpiry overrides the fail-safe expiry window for tests. Passing
// nil restores the default window.
func (m *Checkpoints) SetCheckpointExpiry(fn func() time.Duration) {
	if fn == nil {
		fn = func() time.Duration { return DefaultCheckpointTimeout }
	}
	m.expiryFor = fn
}

// RestorePending restores pending checkpoints with their original expiry deadlines.
func (m *Checkpoints) RestorePending(ctx context.Context) error {
	rows, err := m.Store.ListPending(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, row := range rows {
		d := m.expiryFor()
		if d > 0 {
			remaining := row.CreatedAt.Add(d).Sub(now)
			if remaining <= 0 {
				if err := m.expirePending(ctx, row.ID, "approval request timed out — denied (fail-safe)"); err != nil && !errors.Is(err, ErrCheckpointNotFound) {
					return err
				}
				continue
			}
			d = remaining
		}
		m.Authority.restoreToolApproval(row)
		m.expiries.schedule(ctx, row.ID, d, m.expirePending)
	}
	return nil
}

// expirePending serializes expiry with human resolution and skips resolved rows.
func (m *Checkpoints) expirePending(ctx context.Context, checkpointID, reason string) error {
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	row, err := m.Store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if row.Status != DecisionStatusPending {
		return nil
	}
	result := DecisionResult{Approved: false, Comments: reason}
	now := time.Now().UTC()
	resolution := Resolution{By: authzledger.ResolvedByExpiry}
	viaOutbox, err := m.Store.resolveCheckpoint(ctx, *row, DecisionStatusExpired, &result, nil, now, resolution,
		m.Authority.resolutionSeal(ctx, DecisionStatusExpired))
	if err != nil {
		return err
	}
	row.Status = DecisionStatusExpired
	row.Result = &result
	row.ResolvedAt = &now
	row.Resolution = &resolution
	m.announceResolved(ctx, *row, viaOutbox)
	if row.Kind == api.CheckpointKindToolApproval {
		// Expiry releases coalescing without recording a denial.
		m.Authority.notifyToolApprovalTerminal(*row, DecisionStatusExpired)
	}
	return nil
}

// RequestCheckpoint creates a pending checkpoint and publishes SSE.
func (m *Checkpoints) RequestCheckpoint(ctx context.Context, req CheckpointRequest) (*CheckpointResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.SessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	if req.Kind == "" {
		return nil, fmt.Errorf("kind required")
	}
	projectID, err := m.checkpointProject(ctx, req)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	id := uuid.NewString()
	row := StoredCheckpoint{
		ID:          id,
		SessionID:   req.SessionID,
		ProjectID:   projectID,
		Kind:        req.Kind,
		Status:      DecisionStatusPending,
		Type:        req.Type,
		Title:       req.Title,
		Description: req.Description,
		CreatedAt:   now,
		Payload:     map[string]any{},
	}
	if row.Type == "" {
		row.Type = DecisionTypeApprove
	}
	if req.Kind == api.CheckpointKindToolApproval {
		plan := req.ApprovalPlan
		if plan == nil {
			var err error
			plan, err = CompileCheckpointApprovalPlan(req)
			if err != nil {
				return nil, err
			}
		}
		if req.ProposedAction == nil || plan.ActionDigest == "" || plan.ActionDigest != GrantKey(*req.ProposedAction) {
			return nil, fmt.Errorf("approval plan action does not match proposed action")
		}
		stored, err := storeApprovalPlan(plan)
		if err != nil {
			return nil, err
		}
		row.Payload["approval_plan"] = stored
		seedPayloadConsequence(&row, plan)
		seedPayloadDetection(&row, req)
	}
	switch req.Kind {
	case api.CheckpointKindToolApproval:
		if err := applyToolApprovalRequest(&row, req); err != nil {
			return nil, err
		}
	case api.CheckpointKindContentApply:
		if req.ContentApply == nil {
			return nil, fmt.Errorf("content_apply payload required")
		}
		plan, err := CompileContentApplyPlan(*req.ContentApply)
		if err != nil {
			return nil, err
		}
		stored, err := storeContentApplyPlan(plan)
		if err != nil {
			return nil, err
		}
		row.ToolName = plan.Tool
		row.Path = plan.Path
		if row.Title == "" {
			row.Title = fmt.Sprintf("Review edit: %s", plan.Path)
		}
		row.Payload["content_apply_plan"] = stored
	default:
		return nil, fmt.Errorf("unsupported checkpoint kind %q", req.Kind)
	}
	if err := m.Store.Insert(ctx, row); err != nil {
		return nil, err
	}
	m.publishEvent(ctx, row)
	if m.expiryFor != nil {
		m.expiries.schedule(ctx, id, m.expiryFor(), m.expirePending)
	}
	return storedToCheckpointResponse(&row), nil
}

// checkpointProject validates declared ownership against the persisted session.
func (m *Checkpoints) checkpointProject(ctx context.Context, req CheckpointRequest) (string, error) {
	projectID, err := m.Store.SessionProjectID(ctx, req.SessionID)
	if err != nil {
		return "", fmt.Errorf("checkpoint session project: %w", err)
	}
	if req.ProjectID != "" && req.ProjectID != projectID {
		return "", fmt.Errorf("checkpoint project differs from session")
	}
	if action := req.ProposedAction; action != nil {
		if action.Scope.ProjectID != "" && action.Scope.ProjectID != projectID {
			return "", fmt.Errorf("checkpoint action project differs from session")
		}
		if action.Scope.SessionID != "" && action.Scope.SessionID != req.SessionID {
			return "", fmt.Errorf("checkpoint action session differs from request")
		}
	}
	return projectID, nil
}

// PollCheckpoint waits for authority installation to settle.
func (m *Checkpoints) PollCheckpoint(ctx context.Context, checkpointID string) (*CheckpointResponse, error) {
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	row, err := m.Store.Get(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	return storedToCheckpointResponse(row), nil
}

// ResolveCheckpoint applies a human resolution.
func (m *Checkpoints) ResolveCheckpoint(ctx context.Context, sessionID, checkpointID string, kind api.CheckpointKind, toolResult *DecisionResult, contentResult *ContentApplyResolve) (*CheckpointResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.Sessions.admission != nil {
		var response *CheckpointResponse
		err := m.Sessions.admission(ctx, sessionID, func() error {
			var resolveErr error
			response, resolveErr = m.resolveCheckpointAdmitted(ctx, sessionID, checkpointID, kind, toolResult, contentResult)
			return resolveErr
		})
		return response, err
	}
	return m.resolveCheckpointAdmitted(ctx, sessionID, checkpointID, kind, toolResult, contentResult)
}

func (m *Checkpoints) resolveCheckpointAdmitted(ctx context.Context, sessionID, checkpointID string, kind api.CheckpointKind, toolResult *DecisionResult, contentResult *ContentApplyResolve) (*CheckpointResponse, error) {
	if kind == api.CheckpointKindContentApply {
		return m.resolveContentApply(ctx, sessionID, checkpointID, contentResult)
	}
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	row, err := m.Store.Get(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if row.SessionID != sessionID {
		return nil, ErrCheckpointNotFound
	}
	if row.Kind != kind {
		return nil, ErrCheckpointKindMismatch
	}
	if kind == api.CheckpointKindToolApproval && toolResult != nil && toolResult.Approved {
		return nil, ErrApprovalOptionRequired
	}
	status := decisionStatusFromResolve(kind, toolResult, contentResult)
	if row.Status != DecisionStatusPending {
		if row.Status == status && reflect.DeepEqual(row.Result, toolResult) && reflect.DeepEqual(row.ContentResult, contentResult) {
			return storedToCheckpointResponse(row), nil
		}
		return nil, ErrCheckpointNotPending
	}
	resolution, err := personResolution(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	viaOutbox, err := m.Store.resolveCheckpoint(ctx, *row, status, toolResult, contentResult, now, resolution,
		m.Authority.resolutionSeal(ctx, status))
	if err != nil {
		return nil, err
	}
	row.Status = status
	row.Result = toolResult
	row.ContentResult = contentResult
	row.ResolvedAt = &now
	row.Resolution = &resolution
	m.announceResolved(ctx, *row, viaOutbox)
	if row.Kind == api.CheckpointKindToolApproval {
		m.Authority.notifyToolApprovalTerminal(*row, status)
	}
	return storedToCheckpointResponse(row), nil
}

// PatchPendingToolApprovalAIRationale updates and republishes only still-pending cards.
func (m *Checkpoints) PatchPendingToolApprovalAIRationale(ctx context.Context, checkpointID, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if checkpointID == "" || text == "" {
		return nil
	}
	applied, err := m.Store.SetPendingAIRationale(ctx, checkpointID, text)
	if err != nil || !applied || m.Store.EventsViaOutbox() {
		return err
	}
	row, err := m.Store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	// Re-check after load: a concurrent resolve can win between the write and Get.
	if row.Status != DecisionStatusPending {
		return nil
	}
	if row.Kind != api.CheckpointKindToolApproval {
		return nil
	}
	m.publishEvent(ctx, *row)
	return nil
}

// ClearPendingToolApprovalAIRationale clears the pending flag after an empty summary.
// Only a still-pending checkpoint is updated and republished.
func (m *Checkpoints) ClearPendingToolApprovalAIRationale(ctx context.Context, checkpointID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if checkpointID == "" {
		return nil
	}
	applied, err := m.Store.ClearPendingAIRationaleFlag(ctx, checkpointID)
	if err != nil || !applied || m.Store.EventsViaOutbox() {
		return err
	}
	row, err := m.Store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if row.Status != DecisionStatusPending || row.Kind != api.CheckpointKindToolApproval {
		return nil
	}
	m.publishEvent(ctx, *row)
	return nil
}

// PatchPendingToolApprovalJoined bumps joined_count / joined_tool_call_ids on a
// still-pending tool_approval checkpoint and republishes SSE when applied. It
// holds the card's resolution lock, so a joiner registers before the decision
// commits or gets ErrCheckpointNotPending.
func (m *Checkpoints) PatchPendingToolApprovalJoined(ctx context.Context, checkpointID string, joinedCount int, joinedToolCallIDs []string, joinerBand string, joinerCode string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if checkpointID == "" {
		return nil
	}
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	applied, err := m.Store.SetPendingJoined(ctx, checkpointID, joinedCount, joinedToolCallIDs, joinerBand, joinerCode)
	if err != nil {
		return err
	}
	if !applied {
		return ErrCheckpointNotPending
	}
	if m.Store.EventsViaOutbox() {
		return nil
	}
	row, err := m.Store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if row.Status != DecisionStatusPending || row.Kind != api.CheckpointKindToolApproval {
		return nil
	}
	m.publishEvent(ctx, *row)
	return nil
}

// ListPending returns pending checkpoints for a session.
func (m *Checkpoints) ListPending(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	rows, err := m.Store.ListBySession(ctx, sessionID, DecisionStatusPending, kind)
	if err != nil {
		return nil, err
	}
	out := make([]api.CheckpointEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredCheckpointToEvent(row))
	}
	return out, nil
}

// OldestPendingCheckpoints reports, per session, when the oldest live human
// checkpoint was issued. It feeds the cross-project attention view in one read.
func (m *Checkpoints) OldestPendingCheckpoints(ctx context.Context) (map[string]time.Time, error) {
	return m.Store.OldestPendingBySession(ctx)
}

// SessionApprovalDenied is true when the latest resolved tool_approval was rejected.
func (m *Checkpoints) SessionApprovalDenied(ctx context.Context, sessionID string) (bool, error) {
	status, ok, err := m.Store.LatestResolvedToolApprovalStatus(ctx, sessionID)
	if err != nil || !ok {
		return false, err
	}
	return status == DecisionStatusRejected || status == DecisionStatusExpired, nil
}

// ListPendingForParent includes worker checkpoints without enumerating old workers.
func (m *Checkpoints) ListPendingForParent(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	rows, err := m.Store.ListPendingForParent(ctx, sessionID, kind)
	if err != nil {
		return nil, err
	}
	out := make([]api.CheckpointEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredCheckpointToEvent(row))
	}
	return out, nil
}
