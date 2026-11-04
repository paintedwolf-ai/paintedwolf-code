package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type overlayTaskLookup interface {
	Get(jobID string) (*api.WorkerTask, bool)
}

// sealedOverlayPublisher states a finished materialized parent's overlay as a checkpoint.
type sealedOverlayPublisher interface {
	PublishSealedOverlayCheckpoint(ctx context.Context, sealed workspace.SealedOverlay) (string, error)
}

// stackedParentCheckpoint names the immutable checkpoint a stacked worker starts from. A worker whose
// base already landed, or that has no base, starts from the live project. A parent still producing
// its result has nothing immutable to stack on: its working tree is never a source.
func stackedParentCheckpoint(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef, overlays overlayTaskLookup, publisher sealedOverlayPublisher) (string, error) {
	if task == nil {
		return "", fmt.Errorf("worker task required")
	}
	baseID := strings.TrimSpace(task.EffectiveScope().BaseOverlayID)
	if baseID == "" {
		return "", nil
	}
	if overlays == nil {
		return "", fmt.Errorf("base overlay %q is unavailable", baseID)
	}
	base, ok := overlays.Get(baseID)
	if !ok || base == nil {
		return "", fmt.Errorf("base overlay %q is unavailable", baseID)
	}
	if base.ID == task.ID || base.ProjectID != task.ProjectID || base.ParentSessionID != task.ParentSessionID {
		return "", fmt.Errorf("base overlay %q does not belong to this worker lineage", baseID)
	}
	switch base.MergeStatus {
	case api.WorkerMergeStatusMerged:
		return "", nil
	case api.WorkerMergeStatusRejected, api.WorkerMergeStatusOrphaned, api.WorkerMergeStatusAborted:
		return "", fmt.Errorf("base overlay %q is %s", baseID, base.MergeStatus)
	case api.WorkerMergeStatusPending, api.WorkerMergeStatusApplying, api.WorkerMergeStatusRebasing:
	}
	if !base.EffectiveScope().IsWrite() {
		return "", fmt.Errorf("base overlay %q has no complete branch", baseID)
	}
	if overlay := strings.TrimSpace(base.WorkspaceOverlayPath); overlay != "" {
		if publisher == nil {
			return "", fmt.Errorf("base overlay %q has no checkpoint store", baseID)
		}
		// The parent's result is its delta over its own parent, so that link joins the chain.
		grandparent, err := stackedParentCheckpoint(ctx, base, roots, overlays, publisher)
		if err != nil {
			return "", err
		}
		return publisher.PublishSealedOverlayCheckpoint(ctx, workspace.SealedOverlay{
			JobID: base.ID, ProjectID: base.ProjectID, BaselinePath: base.WorkspaceBaselinePath, OverlayPath: overlay, Roots: roots,
			ParentCheckpointID: grandparent,
		})
	}
	if _, checkpointID, published := workspace.BranchCheckpoint(base.WorkspaceRoot); published {
		return checkpointID, nil
	}
	return "", fmt.Errorf("base overlay %q has no published result to stack on", baseID)
}
