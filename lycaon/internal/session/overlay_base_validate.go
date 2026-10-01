package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Structured rejection codes raised at task() spawn when a stacked-overlay
// scope declares an invalid BaseOverlayID. Their policy units are
// lycaon/config/packs/painted-wolf/security/policy/OVERLAY_BASE_*.yaml.
const (
	OverlayBaseMissingCode    = "OVERLAY_BASE_MISSING"
	OverlayBaseNotPendingCode = "OVERLAY_BASE_NOT_PENDING"
)

// OverlayBaseRejection is the structured outcome of stacked-base validation
// at task() spawn. Code is empty when the base is valid (or unset for non-stacked).
type OverlayBaseRejection struct {
	Code string
	Data map[string]any
}

// ValidateStackedBase requires an active write overlay on the same parent session.
// An omitted base selects primary.
func ValidateStackedBase(
	ctx context.Context,
	list WorkerCycleLister,
	parentSessionID, projectDir string,
	scope api.TaskScope,
) (OverlayBaseRejection, error) {
	base := strings.TrimSpace(scope.BaseOverlayID)
	if base == "" {
		return OverlayBaseRejection{}, nil
	}
	if list == nil {
		return OverlayBaseRejection{
			Code: OverlayBaseMissingCode,
			Data: map[string]any{"base_overlay_id": base},
		}, nil
	}
	// The base overlay's job ID equals its overlay ID; a valid base is a
	// non-terminal write overlay on this parent session.
	task, ok := list.Get(base)
	if !ok || task == nil {
		return OverlayBaseRejection{
			Code: OverlayBaseMissingCode,
			Data: map[string]any{"base_overlay_id": base},
		}, nil
	}
	if strings.TrimSpace(task.ParentSessionID) != strings.TrimSpace(parentSessionID) {
		return OverlayBaseRejection{
			Code: OverlayBaseMissingCode,
			Data: map[string]any{"base_overlay_id": base, "reason": "wrong_parent_session"},
		}, nil
	}
	if !task.EffectiveScope().IsWrite() {
		return OverlayBaseRejection{
			Code: OverlayBaseMissingCode,
			Data: map[string]any{"base_overlay_id": base, "reason": "base_is_read_scope"},
		}, nil
	}
	switch task.MergeStatus {
	case api.WorkerMergeStatusPending,
		api.WorkerMergeStatusApplying,
		api.WorkerMergeStatusRebasing,
		"":
		return OverlayBaseRejection{}, nil
	case api.WorkerMergeStatusMerged:
		return OverlayBaseRejection{
			Code: OverlayBaseNotPendingCode,
			Data: map[string]any{"base_overlay_id": base, "merge_status": string(task.MergeStatus), "reason": "already_promoted"},
		}, nil
	case api.WorkerMergeStatusOrphaned,
		api.WorkerMergeStatusRejected,
		api.WorkerMergeStatusAborted:
		return OverlayBaseRejection{
			Code: OverlayBaseNotPendingCode,
			Data: map[string]any{"base_overlay_id": base, "merge_status": string(task.MergeStatus), "reason": "base_closed"},
		}, nil
	default:
		return OverlayBaseRejection{
			Code: OverlayBaseNotPendingCode,
			Data: map[string]any{"base_overlay_id": base, "merge_status": string(task.MergeStatus)},
		}, nil
	}
}
