package session

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

type registeredOverlayObservation struct {
	tracker      *observation.Tracker
	binding      observation.VerificationBinding
	checkpointID string
}

var (
	overlayObsMu       sync.RWMutex
	overlayObsRegistry = make(map[string]registeredOverlayObservation)
)

// OverlayObservationAssessment summarizes receipt freshness, trunk.changed notices,
// and stale-observation promotion preview hints for a worker overlay.
type OverlayObservationAssessment struct {
	Freshness    observation.ReceiptFreshnessResult `json:"freshness"`
	TrunkChanged *observation.TrunkChangedNotice    `json:"trunk_changed,omitempty"`
	PreviewHints []observation.StaleObservationHint `json:"preview_hints,omitempty"`
}

// RegisterOverlayObservationTracker binds an observation.Tracker and VerificationBinding to a
// worker task key (job ID, child session ID, workspace root, or workspace path).
func RegisterOverlayObservationTracker(
	key string,
	tracker *observation.Tracker,
	binding observation.VerificationBinding,
	currentCheckpointID string,
) {
	key = strings.TrimSpace(key)
	if key == "" || tracker == nil {
		return
	}
	overlayObsMu.Lock()
	defer overlayObsMu.Unlock()
	overlayObsRegistry[key] = registeredOverlayObservation{
		tracker:      tracker,
		binding:      binding,
		checkpointID: strings.TrimSpace(currentCheckpointID),
	}
}

// UnregisterOverlayObservationTracker removes a bound tracker from the registry.
func UnregisterOverlayObservationTracker(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	overlayObsMu.Lock()
	defer overlayObsMu.Unlock()
	delete(overlayObsRegistry, key)
}

// OverlayObservationAssessmentForTask evaluates receipt freshness and promotion preview hints
// for task when an observation tracker is registered.
func OverlayObservationAssessmentForTask(task *api.WorkerTask) (OverlayObservationAssessment, bool) {
	reg, ok := lookupOverlayObservation(task)
	if !ok {
		return OverlayObservationAssessment{}, false
	}
	_, assessment := EvaluateOverlaySourceObservations(
		workercompletion.WorkerCompletionProof{},
		reg.tracker,
		reg.binding,
		reg.checkpointID,
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
	}
	hints := tracker.BuildPromotionPreviewHintsForBinding(binding)
	if freshness.Stale && len(proof.InvocationReceipts) > 0 {
		updated := make([]workercompletion.WorkerInvocationReceipt, len(proof.InvocationReceipts))
		copy(updated, proof.InvocationReceipts)
		for i := range updated {
			if binding.ReceiptID == "" || updated[i].ID == binding.ReceiptID || updated[i].CheckID == binding.ReceiptID {
				updated[i].SourceRevision = "stale:" + freshness.StaleReason + ":" + updated[i].SourceRevision
			}
		}
		proof.InvocationReceipts = updated
	}
	return proof, OverlayObservationAssessment{
		Freshness:    freshness,
		TrunkChanged: notice,
		PreviewHints: hints,
	}
}

// OverlaySourceProof binds worker receipts to the overlay's current source content and
// invalidates receipts made stale by observed trunk changes, watcher overflow, or incomplete attribution.
func OverlaySourceProof(
	ctx context.Context,
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
	if reg, ok := lookupOverlayObservation(task); ok {
		proof, _ = EvaluateOverlaySourceObservations(proof, reg.tracker, reg.binding, reg.checkpointID)
	}
	return proof
}

func lookupOverlayObservation(task *api.WorkerTask) (registeredOverlayObservation, bool) {
	if task == nil {
		return registeredOverlayObservation{}, false
	}
	overlayObsMu.RLock()
	defer overlayObsMu.RUnlock()
	for _, key := range []string{task.ID, task.ChildSessionID, task.WorkspaceRoot, task.WorkspacePath} {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if reg, ok := overlayObsRegistry[key]; ok && reg.tracker != nil {
			return reg, true
		}
	}
	return registeredOverlayObservation{}, false
}
