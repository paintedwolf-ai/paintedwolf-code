package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// OverlayObservationAssessment summarizes receipt freshness, trunk.changed notices,
// and stale-observation promotion preview hints for a worker overlay.
type OverlayObservationAssessment struct {
	Freshness               observation.ReceiptFreshnessResult `json:"freshness"`
	VerificationInvalidated bool                               `json:"verification_invalidated"`
	StaleSourcePaths        []layeredfs.QualifiedPath          `json:"stale_source_paths,omitempty"`
	TrunkChanged            *observation.TrunkChangedNotice    `json:"trunk_changed,omitempty"`
	PreviewHints            []observation.StaleObservationHint `json:"preview_hints,omitempty"`
}

// OverlayObservationAssessmentWithWorkspaces evaluates receipt freshness and promotion preview hints
// for task against the scoped tracker owned by workspaces.
func OverlayObservationAssessmentWithWorkspaces(
	workspaces *workerworkspace.Service,
	task *api.WorkerTask,
) (OverlayObservationAssessment, bool) {
	if workspaces == nil || task == nil {
		return OverlayObservationAssessment{}, false
	}
	tracker, binding, checkpointID, ok := workspaces.LatestBindingFor(
		task.ID,
		task.ChildSessionID,
		task.WorkspaceRoot,
		task.WorkspacePath,
	)
	if !ok || tracker == nil {
		return OverlayObservationAssessment{}, false
	}
	_, assessment := EvaluateOverlaySourceObservations(
		workercompletion.WorkerCompletionProof{},
		tracker,
		binding,
		checkpointID,
	)
	return assessment, true
}

// EvaluateOverlaySourceObservations evaluates whether observed source changes across any
// directory/root, watcher overflow uncertainty, or incomplete attribution with advanced scope
// generation invalidate a worker's verification receipts.
func EvaluateOverlaySourceObservations(
	proof workercompletion.WorkerCompletionProof,
	tracker *observation.Tracker,
	binding observation.VerificationBinding,
	currentCheckpointID string,
) (workercompletion.WorkerCompletionProof, OverlayObservationAssessment) {
	if tracker == nil {
		return proof, OverlayObservationAssessment{}
	}
	freshness := tracker.EvaluateReceiptFreshness(binding, currentCheckpointID)
	var notice *observation.TrunkChangedNotice
	if n, ok := tracker.PendingTrunkChangedNotice(); ok {
		notice = n
	} else if n, ok := tracker.LastTrunkChangedNotice(); ok {
		notice = n
	}
	hints := tracker.BuildPromotionPreviewHintsForBinding(binding)
	if freshness.Stale && len(proof.InvocationReceipts) > 0 {
		updated := make([]workercompletion.WorkerInvocationReceipt, len(proof.InvocationReceipts))
		copy(updated, proof.InvocationReceipts)
		matched := false
		for i := range updated {
			if binding.ReceiptID == "" || updated[i].ID == binding.ReceiptID || updated[i].CheckID == binding.ReceiptID {
				updated[i].SourceRevision = "stale:" + freshness.StaleReason + ":" + updated[i].SourceRevision
				matched = true
			}
		}
		if !matched {
			for i := range updated {
				updated[i].SourceRevision = "stale:" + freshness.StaleReason + ":" + updated[i].SourceRevision
			}
		}
		proof.InvocationReceipts = updated
	}
	return proof, OverlayObservationAssessment{
		Freshness:               freshness,
		VerificationInvalidated: freshness.Stale,
		StaleSourcePaths:        freshness.InvalidatingPaths,
		TrunkChanged:            notice,
		PreviewHints:            hints,
	}
}

// OverlaySourceProofWithWorkspaces binds worker receipts to the overlay's current source content and
// invalidates receipts made stale by observed trunk changes, watcher overflow, or incomplete attribution
// using the scoped tracker owned by workspaces.
func OverlaySourceProofWithWorkspaces(
	ctx context.Context,
	workspaces *workerworkspace.Service,
	task *api.WorkerTask,
	childMessages []api.Message,
	declaredCommand string,
	current workercompletion.SourceRevision,
) workercompletion.WorkerCompletionProof {
	if task == nil {
		return workercompletion.WorkerCompletionProof{}
	}
	proof := workercompletion.BuildWorkerCompletionProof(
		childMessages, OverlayWorkspaceChangedPaths(ctx, task, nil),
		current,
	)
	declaredCommand = strings.TrimSpace(declaredCommand)
	proof.DeclaredCommand = &declaredCommand
	if workspaces == nil {
		workspaces = workerworkspace.FromContext(ctx)
	}
	if workspaces != nil {
		if tracker, binding, checkpointID, ok := workspaces.LatestBindingFor(
			task.ID,
			task.ChildSessionID,
			task.WorkspaceRoot,
			task.WorkspacePath,
		); ok && tracker != nil {
			proof, _ = EvaluateOverlaySourceObservations(proof, tracker, binding, checkpointID)
		}
	}
	return proof
}

// OverlaySourceProofForWorkspaces returns a SourceProof function bound to the scoped workspaces service.
func OverlaySourceProofForWorkspaces(
	workspaces *workerworkspace.Service,
) func(context.Context, *api.WorkerTask, []api.Message, string, workercompletion.SourceRevision) workercompletion.WorkerCompletionProof {
	return func(ctx context.Context, task *api.WorkerTask, childMessages []api.Message, declaredCommand string, current workercompletion.SourceRevision) workercompletion.WorkerCompletionProof {
		return OverlaySourceProofWithWorkspaces(ctx, workspaces, task, childMessages, declaredCommand, current)
	}
}

// OverlaySourceProof binds worker receipts to the overlay's current source content and
// evaluates any scoped workspace tracker carried in ctx.
func OverlaySourceProof(
	ctx context.Context,
	task *api.WorkerTask,
	childMessages []api.Message,
	declaredCommand string,
	current workercompletion.SourceRevision,
) workercompletion.WorkerCompletionProof {
	return OverlaySourceProofWithWorkspaces(ctx, workerworkspace.FromContext(ctx), task, childMessages, declaredCommand, current)
}
