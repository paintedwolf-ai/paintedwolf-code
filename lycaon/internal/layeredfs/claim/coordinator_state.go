package claim

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func (c *Coordinator) recordFailedClaim(
	req ClaimRequest,
	adpID adapter.AdapterID,
	reason adapter.ReasonCode,
	diag string,
	entries, bytes int64,
) WorkspaceStateSnapshot {
	snap := buildFailedPreparationSnapshot(req, adpID, reason, diag, entries, bytes)
	c.mu.Lock()
	c.failedSnapshots[req.Identity.ViewID] = snap
	c.mu.Unlock()
	return snap
}

// StateSnapshot returns the current typed WorkspaceStateSnapshot for viewID.
func (c *Coordinator) StateSnapshot(viewID string) (WorkspaceStateSnapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if cv, ok := c.views[viewID]; ok {
		return cv.State, nil
	}
	if fs, ok := c.failedSnapshots[viewID]; ok {
		return fs, nil
	}
	return WorkspaceStateSnapshot{}, ErrViewNotFound
}

// SetCommandState updates the live/completed command execution state for viewID while
// keeping workspace readiness and checkpoint persistence strictly distinct (#1662).
func (c *Coordinator) SetCommandState(viewID string, cmdState CommandState) (WorkspaceStateSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.views[viewID]
	if !ok {
		return WorkspaceStateSnapshot{}, ErrViewNotFound
	}
	cv.State.Command = cmdState
	return cv.State, nil
}

// RecordCheckpointPending records an in-flight checkpoint capture on viewID without altering readiness.
func (c *Coordinator) RecordCheckpointPending(viewID, checkpointID string) (WorkspaceStateSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.views[viewID]
	if !ok {
		return WorkspaceStateSnapshot{}, ErrViewNotFound
	}
	cv.Metadata.Checkpoints.PendingCheckpointID = checkpointID
	cv.Metadata.Checkpoints.FailedCheckpointReason = adapter.ReasonNone
	cv.State.Checkpoint.State = CheckpointPending
	cv.State.Checkpoint.PendingCheckpointID = checkpointID
	cv.State.Checkpoint.FailedReasonCode = adapter.ReasonNone
	cv.State.Checkpoint.Diagnostic = ""
	cv.State.SupportedActions = deriveSupportedActions(cv.State)
	return cv.State, nil
}

// RecordCheckpointPublished records a durably committed checkpoint on viewID and updates its state.
func (c *Coordinator) RecordCheckpointPublished(viewID, checkpointID string) (WorkspaceStateSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.views[viewID]
	if !ok {
		return WorkspaceStateSnapshot{}, ErrViewNotFound
	}
	cv.Metadata.Checkpoints.LastPublishedCheckpointID = checkpointID
	cv.Metadata.Checkpoints.PendingCheckpointID = ""
	cv.Metadata.Checkpoints.FailedCheckpointReason = adapter.ReasonNone
	cv.State.Checkpoint = CheckpointStatus{
		State:              CheckpointPublished,
		LatestCheckpointID: checkpointID,
	}
	cv.State.SupportedActions = deriveSupportedActions(cv.State)
	return cv.State, nil
}

// RecordCheckpointFailure records a failed checkpoint persistence on viewID without marking
// the workspace unready, and surfaces ActionRetryCheckpoint in SupportedActions (#1662).
func (c *Coordinator) RecordCheckpointFailure(
	viewID, checkpointID string,
	reason adapter.ReasonCode,
	diagnostic string,
) (WorkspaceStateSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.views[viewID]
	if !ok {
		return WorkspaceStateSnapshot{}, ErrViewNotFound
	}
	cv.Metadata.Checkpoints.PendingCheckpointID = checkpointID
	cv.Metadata.Checkpoints.FailedCheckpointReason = reason
	cv.State.Checkpoint.State = CheckpointFailedPersistence
	cv.State.Checkpoint.PendingCheckpointID = checkpointID
	cv.State.Checkpoint.FailedReasonCode = reason
	cv.State.Checkpoint.Diagnostic = diagnostic
	cv.State.Notice = FallbackNotice{
		Severity:   SeverityWarning,
		ReasonCode: reason,
		Diagnostic: diagnostic,
	}
	cv.State.SupportedActions = deriveSupportedActions(cv.State)
	return cv.State, nil
}

// RecordGitInitState updates the private Git initialization state for viewID (#1662, #1671).
func (c *Coordinator) RecordGitInitState(
	viewID string,
	gitState GitInitState,
	reason adapter.ReasonCode,
	diagnostic string,
) (WorkspaceStateSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.views[viewID]
	if !ok {
		return WorkspaceStateSnapshot{}, ErrViewNotFound
	}
	cv.State.GitInit = gitState
	cv.Metadata.Adapter.PendingGitInit = gitState == GitInitLazyPending
	if gitState == GitInitFailed {
		cv.State.Notice = FallbackNotice{
			Severity:   SeverityWarning,
			ReasonCode: reason,
			Diagnostic: diagnostic,
		}
	}
	cv.State.SupportedActions = deriveSupportedActions(cv.State)
	return cv.State, nil
}

// RecordViewRecoveryRequired transitions viewID to StateRecoveryRequired and exposes ActionRecoverView.
func (c *Coordinator) RecordViewRecoveryRequired(
	viewID string,
	reason adapter.ReasonCode,
	diagnostic string,
) (WorkspaceStateSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.views[viewID]
	if !ok {
		return WorkspaceStateSnapshot{}, ErrViewNotFound
	}
	cv.State.Readiness = StateRecoveryRequired
	cv.State.Notice = FallbackNotice{
		Severity:   SeverityError,
		ReasonCode: reason,
		Diagnostic: diagnostic,
	}
	cv.State.SupportedActions = deriveSupportedActions(cv.State)
	return cv.State, nil
}

// UpdateFocusPathHints updates task-path focus hints on viewID without altering SelectedRoots
// or workspace filesystem content (#1662).
func (c *Coordinator) UpdateFocusPathHints(viewID string, focusHints []string) (WorkspaceStateSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.views[viewID]
	if !ok {
		return WorkspaceStateSnapshot{}, ErrViewNotFound
	}
	copied := append([]string(nil), focusHints...)
	cv.Metadata.FocusPathHints = copied
	cv.State.FocusPathHints = copied
	if cv.Prepared != nil {
		cv.Prepared.TaskPathHints = copied
	}
	return cv.State, nil
}

// ReleaseWorkerView settles and releases the claimed view and its retention claim.
func (c *Coordinator) ReleaseWorkerView(ctx context.Context, viewID string, retention RetentionRegistrar) error {
	c.mu.Lock()
	cv, ok := c.views[viewID]
	if ok {
		delete(c.views, viewID)
	}
	c.mu.Unlock()
	if !ok {
		return nil
	}
	if retention != nil && cv.RetentionClaimID != "" {
		_ = retention.ReleaseClaim(cv.RetentionClaimID)
	}
	if cv.Adapter != nil {
		_, err := cv.Adapter.Release(ctx, viewID)
		return err
	}
	return nil
}

// LoadOrUpgradeRetainedView loads a Format-v2 or Format-v1 metadata file and returns its
// reconstructed ClaimedView state without converting legacy materialized views into live mounts (#1662).
func (c *Coordinator) LoadOrUpgradeRetainedView(metaPath string) (*ClaimedView, bool, error) {
	meta, upgraded, err := LoadViewMetadata(metaPath)
	if err != nil {
		return nil, false, err
	}
	snap := buildReadySnapshot(meta)
	cv := &ClaimedView{
		Identity: meta.Identity,
		Metadata: meta,
		State:    snap,
	}
	return cv, upgraded, nil
}

func checkStageBoundary(ctx context.Context, fault FaultHook, stage ClaimStage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fault != nil {
		if err := fault(ctx, stage); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func resolveMetadataPath(st ClaimStorage, runtimeViewDir string) string {
	if p := strings.TrimSpace(st.MetadataPath); p != "" {
		return p
	}
	return filepath.Join(runtimeViewDir, "view-metadata.json")
}

func collectPreparedRootPaths(roots []adapter.PreparedRoot) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, r.WorkspacePath)
	}
	return out
}

func classifyClaimReason(err error) adapter.ReasonCode {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return adapter.ReasonContextCanceled
	}
	var aErr *adapter.AdapterError
	if errors.As(err, &aErr) && aErr.ReasonCode != adapter.ReasonNone {
		return aErr.ReasonCode
	}
	var limErr *adapter.PreparationLimitError
	if errors.As(err, &limErr) {
		return adapter.ReasonPreparationLimit
	}
	return adapter.ReasonMountDenied
}

func classifyPrepareError(err error, fallbackReason adapter.ReasonCode) (adapter.ReasonCode, string, int64, int64) {
	var limErr *adapter.PreparationLimitError
	if errors.As(err, &limErr) {
		return adapter.ReasonPreparationLimit, limErr.Diagnostic, limErr.ObservedEntries, limErr.ObservedBytes
	}
	reason := classifyClaimReason(err)
	if reason == adapter.ReasonNone {
		reason = fallbackReason
	}
	return reason, err.Error(), 0, 0
}
