package recovery

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

// RecoverRequest specifies parameters for recovering a worker view from durable state.
type RecoverRequest struct {
	WorkerID             string
	ExpectedTaskID       string
	ExpectedProjectID    string
	NewViewID            string
	Adapter              adapter.Adapter
	PrepareReq           adapter.PrepareRequest
	PreviousViewID       string
	PreviousBackingDir   string
	FallbackCheckpointID string
}

// RecoveryResult reports the reconstructed worker view and any salvaged interrupted work.
type RecoveryResult struct {
	WorkerID             string
	ViewID               string
	RestoredCheckpointID string
	Manifest             checkpoint.Manifest
	PreparedView         *adapter.PreparedView
	QuarantinedWork      *InterruptedWorkingRecord
	SettledProcesses     ProcessSettlementReceipt
	SettledMount         MountSettlementReceipt
}

// RebuildAdapterRequest specifies switching a worker view to a new Adapter without relabeling live mounts
// or discarding working data.
type RebuildAdapterRequest struct {
	WorkerID            string
	CurrentView         *adapter.PreparedView
	CurrentAdapter      adapter.Adapter
	TargetAdapter       adapter.Adapter
	NewViewID           string
	PrepareReq          adapter.PrepareRequest
	CheckpointID        string
	Publisher           *checkpoint.Publisher
	CaptureBeforeSwitch bool
}

// RecoverWorkerView validates worker identity and journal state, settles any surviving same-boot
// processes/mounts belonging to this worker, salvages incomplete working data, and reconstructs
// a fresh usable PreparedView from the last valid published checkpoint.
func (c *Coordinator) RecoverWorkerView(ctx context.Context, req RecoverRequest) (*RecoveryResult, error) {
	return recoverWorkerView(ctx, c, req)
}

func recoverWorkerView(ctx context.Context, c *Coordinator, req RecoverRequest) (*RecoveryResult, error) {
	if err := injectFault(c, PhasePreparation, "recover_start"); err != nil {
		return nil, err
	}
	j, err := c.LoadWorkerJournal(req.WorkerID)
	if err != nil {
		return nil, err
	}
	if err := validateWorkerOwnership(j, req.ExpectedTaskID, req.ExpectedProjectID); err != nil {
		return nil, err
	}

	prevViewID := req.PreviousViewID
	if prevViewID == "" {
		prevViewID = j.ViewID
	}
	procReceipt, mountReceipt, prevBacking, err := settleOwnedRuntimeForRecovery(c, req.WorkerID, prevViewID, req.PreviousBackingDir)
	if err != nil {
		return nil, err
	}

	cpID := j.LastPublishedCheckpointID
	if cpID == "" {
		cpID = req.FallbackCheckpointID
	}
	if cpID == "" {
		return nil, fmt.Errorf("%w: worker %s has no published checkpoint", ErrNoValidCheckpoint, req.WorkerID)
	}
	manifest, err := c.VerifyCheckpointReconstructible(ctx, cpID)
	if err != nil {
		return nil, err
	}
	if manifest.WorkerID != req.WorkerID && !containsString(j.ParentCheckpointIDs, manifest.CheckpointID) {
		return nil, fmt.Errorf("%w: checkpoint %s belongs to %s, want %s",
			ErrWorkerIdentityMismatch, manifest.CheckpointID, manifest.WorkerID, req.WorkerID)
	}

	quarantined, err := salvageIfInterruptedOrUnsealed(c, &j, prevViewID, prevBacking, manifest)
	if err != nil {
		return nil, err
	}

	view, err := reconstructViewFromManifest(ctx, c, req.WorkerID, req.NewViewID, req.Adapter, req.PrepareReq, manifest)
	if err != nil {
		return nil, err
	}

	j.ViewID = view.ViewID
	j.AdapterID = req.Adapter.ID()
	j.BootID = c.cfg.CurrentBootID
	j.ActivePhase = ""
	j.ActiveOperation = ""
	j.ActivePaths = nil
	j.ActiveUnknownScope = false
	if err := c.SaveWorkerJournal(j); err != nil {
		return nil, err
	}
	if err := c.RecordTransition(req.WorkerID, view.ViewID, PhasePreparation, StateCommitted, manifest.CheckpointID, "recover_worker_view", nil, false); err != nil {
		return nil, err
	}
	return &RecoveryResult{
		WorkerID:             req.WorkerID,
		ViewID:               view.ViewID,
		RestoredCheckpointID: manifest.CheckpointID,
		Manifest:             manifest,
		PreparedView:         view,
		QuarantinedWork:      quarantined,
		SettledProcesses:     procReceipt,
		SettledMount:         mountReceipt,
	}, nil
}

// RebuildWithAdapter reconstructs validated checkpoint state on TargetAdapter before becoming ready,
// never relabeling a live mount or discarding unsealed working data.
func (c *Coordinator) RebuildWithAdapter(ctx context.Context, req RebuildAdapterRequest) (*adapter.PreparedView, error) {
	return rebuildWithAdapter(ctx, c, req)
}

func rebuildWithAdapter(ctx context.Context, c *Coordinator, req RebuildAdapterRequest) (*adapter.PreparedView, error) {
	if req.TargetAdapter == nil || req.WorkerID == "" {
		return nil, fmt.Errorf("recovery: WorkerID and TargetAdapter are required")
	}
	j, err := c.LoadWorkerJournal(req.WorkerID)
	if err != nil {
		return nil, err
	}
	cpID := req.CheckpointID
	if cpID == "" {
		cpID = j.LastPublishedCheckpointID
	}

	if req.CurrentView != nil {
		if req.CurrentView.ViewID == req.NewViewID || (req.PrepareReq.ViewID != "" && req.PrepareReq.ViewID == req.CurrentView.ViewID) {
			return nil, fmt.Errorf("%w: adapter switch must prepare a new view, not relabel live view %s", ErrActiveUnsealedWork, req.CurrentView.ViewID)
		}
		if req.CaptureBeforeSwitch && req.Publisher != nil {
			pubRes, pErr := req.Publisher.Publish(ctx, checkpoint.PublishRequest{
				Trigger: checkpoint.TriggerExplicitCapture,
			})
			if pErr != nil {
				return nil, pErr
			}
			cpID = pubRes.Manifest.CheckpointID
			j.LastPublishedCheckpointID = cpID
			_ = c.SaveWorkerJournal(j)
		} else if cpID != "" {
			if m, vErr := c.VerifyCheckpointReconstructible(ctx, cpID); vErr == nil {
				if hasUnsealedMutationsBeyondCheckpoint(req.CurrentView.BackingDir, m) {
					return nil, fmt.Errorf("%w: view %s has unsealed working mutations beyond %s",
						ErrActiveUnsealedWork, req.CurrentView.ViewID, cpID)
				}
			}
		} else {
			return nil, fmt.Errorf("%w: no checkpoint available to rebuild view %s", ErrNoValidCheckpoint, req.CurrentView.ViewID)
		}
	}

	manifest, err := c.VerifyCheckpointReconstructible(ctx, cpID)
	if err != nil {
		return nil, err
	}

	newView, err := reconstructViewFromManifest(ctx, c, req.WorkerID, req.NewViewID, req.TargetAdapter, req.PrepareReq, manifest)
	if err != nil {
		return nil, err
	}

	if req.CurrentView != nil && req.CurrentAdapter != nil {
		relReceipt, rErr := req.CurrentAdapter.Release(ctx, req.CurrentView.ViewID)
		if rErr != nil || !relReceipt.Settled {
			_, _ = req.TargetAdapter.Release(ctx, newView.ViewID)
			return nil, &UnresolvedSettlementError{
				WorkerID:        req.WorkerID,
				ViewID:          req.CurrentView.ViewID,
				MountBusy:       !relReceipt.Settled,
				Reason:          "previous adapter view failed to settle during adapter switch",
				RetainedCustody: true,
				Cause:           rErr,
			}
		}
		_ = c.RemoveMountIdentity(req.WorkerID, req.CurrentView.ViewID)
		_ = c.RemoveInventoryEntry("inv-runtime-" + req.CurrentView.ViewID)
	}

	j.ViewID = newView.ViewID
	j.AdapterID = req.TargetAdapter.ID()
	if err := c.SaveWorkerJournal(j); err != nil {
		return nil, err
	}
	return newView, nil
}
