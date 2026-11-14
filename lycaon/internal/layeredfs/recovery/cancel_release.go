package recovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

// CancelReleaseRequest specifies parameters for worker cancellation and view release.
type CancelReleaseRequest struct {
	WorkerID        string
	ViewID          string
	View            *adapter.PreparedView
	Adapter         adapter.Adapter
	Publisher       *checkpoint.Publisher
	CaptureOnCancel bool
	CheckpointID    string
}

// CancelReleaseResult reports the outcome of stopping a worker, optionally capturing a checkpoint,
// preserving unsealed work, and settling runtime mounts/handles.
type CancelReleaseResult struct {
	WorkerID              string
	ViewID                string
	CapturedCheckpoint    *checkpoint.Manifest
	PreservedCheckpointID string
	QuarantinedWork       *InterruptedWorkingRecord
	ReleaseReceipt        adapter.ReleaseReceipt
	SettledProcesses      ProcessSettlementReceipt
}

// CancelAndRelease stops only the worker's recorded processes, blocks new mutations, attempts a
// checkpoint when requested (or preserves the last checkpoint and unsealed working data), and
// settles the worker's filesystem connection and runtime resources with a bounded failure path.
func (c *Coordinator) CancelAndRelease(ctx context.Context, req CancelReleaseRequest) (*CancelReleaseResult, error) {
	return cancelAndRelease(ctx, c, req)
}

func cancelAndRelease(ctx context.Context, c *Coordinator, req CancelReleaseRequest) (*CancelReleaseResult, error) {
	viewID := req.ViewID
	if viewID == "" && req.View != nil {
		viewID = req.View.ViewID
	}
	if req.WorkerID == "" || viewID == "" {
		return nil, fmt.Errorf("recovery: WorkerID and ViewID are required")
	}
	if err := c.RecordTransition(req.WorkerID, viewID, PhaseRelease, StateBegin, req.CheckpointID, "cancel_and_release", nil, false); err != nil {
		return nil, err
	}
	if err := injectFault(c, PhaseRelease, "cancel_start"); err != nil {
		return nil, err
	}

	j, err := c.LoadWorkerJournal(req.WorkerID)
	if err != nil {
		return nil, err
	}
	ident, hasIdent, err := c.LoadMountIdentity(req.WorkerID, viewID)
	if err != nil {
		return nil, err
	}
	backingDir := ident.BackingDir
	if backingDir == "" && req.View != nil {
		backingDir = req.View.BackingDir
	}

	var procRec ProcessSettlementReceipt
	if hasIdent {
		procRec, err = settleRecordedProcesses(ctx, c, ident)
		if err != nil {
			return nil, err
		}
	}

	captured, quarantined, err := captureOrPreserveBeforeRelease(ctx, c, req, &j, viewID, backingDir)
	if err != nil {
		return nil, err
	}

	if err := settleMountAndAdapterRelease(ctx, c, req, viewID, backingDir, hasIdent, ident); err != nil {
		return nil, err
	}

	if c.cfg.GitManager != nil {
		_ = c.cfg.GitManager.ReleaseView(viewID)
	}
	if err := errors.Join(c.ReleaseClaim("claim-view-"+viewID), forgetSettledView(c, req.WorkerID, viewID)); err != nil {
		return nil, err
	}

	if err := c.RecordTransition(req.WorkerID, viewID, PhaseRelease, StateCommitted, j.LastPublishedCheckpointID, "cancel_and_release", nil, false); err != nil {
		return nil, err
	}

	return &CancelReleaseResult{
		WorkerID:              req.WorkerID,
		ViewID:                viewID,
		CapturedCheckpoint:    captured,
		PreservedCheckpointID: j.LastPublishedCheckpointID,
		QuarantinedWork:       quarantined,
		ReleaseReceipt:        adapter.ReleaseReceipt{ViewID: viewID, Settled: true},
		SettledProcesses:      procRec,
	}, nil
}

func captureOrPreserveBeforeRelease(
	ctx context.Context,
	c *Coordinator,
	req CancelReleaseRequest,
	j *WorkerLifecycleJournal,
	viewID, backingDir string,
) (*checkpoint.Manifest, *InterruptedWorkingRecord, error) {
	if req.CaptureOnCancel && req.Publisher != nil {
		cpID := req.CheckpointID
		if cpID == "" {
			cpID = fmt.Sprintf("cp-cancel-%s-%d", req.WorkerID, c.seq.Add(1))
		}
		var pgSnap checkpoint.PrivateGitSnapshot
		if c.cfg.GitManager != nil {
			if st, gErr := c.cfg.GitManager.CaptureCheckpointGitState(ctx, viewID, cpID); gErr == nil && st != nil {
				pgSnap = checkpoint.PrivateGitSnapshot{
					HeadRef:         st.HeadSymbolic,
					Refs:            st.Refs,
					RetainedObjects: st.Artifact.ReachableOIDs,
				}
			}
		}
		pubRes, pErr := req.Publisher.Publish(ctx, checkpoint.PublishRequest{
			CheckpointID: cpID,
			Trigger:      checkpoint.TriggerExplicitCapture,
			PrivateGit:   pgSnap,
		})
		if pErr == nil {
			m := pubRes.Manifest
			j.LastPublishedCheckpointID = m.CheckpointID
			if err := c.CommitCheckpointRetention(ctx, req.WorkerID, m.CheckpointID, nil, m.PrivateGit.RetainedObjects, m.ParentCheckpoints); err != nil {
				return nil, nil, err
			}
			if err := c.SaveWorkerJournal(*j); err != nil {
				return nil, nil, err
			}
			return &m, nil, nil
		}
	}

	if req.View != nil && req.View.Core != nil {
		if tok, bErr := req.View.Core.BeginCheckpointBarrier(ctx); bErr == nil {
			_ = req.View.Core.EndCheckpointBarrier(tok)
		} else if !errors.Is(bErr, layeredfs.ErrBarrierActive) && !errors.Is(bErr, layeredfs.ErrViewReleased) {
			return nil, nil, bErr
		}
	}

	var lastManifest checkpoint.Manifest
	if j.LastPublishedCheckpointID != "" {
		lastManifest, _ = c.cfg.CheckpointStore.LoadManifest(j.LastPublishedCheckpointID)
	}
	if hasUnsealedMutationsBeyondCheckpoint(backingDir, lastManifest) {
		rec, qErr := c.PreserveInterruptedWorkingStorage(
			req.WorkerID, viewID, j.LastPublishedCheckpointID, backingDir,
			PhaseRelease, "cancel_preserve_unsealed", nil, false,
			"unsealed working data preserved on cancellation",
		)
		if qErr != nil {
			return nil, nil, qErr
		}
		return nil, rec, nil
	}
	return nil, nil, nil
}

// settleMountAndAdapterRelease releases a view this process serves through its adapter. A view
// from an earlier process, or one a retry already released, settles by its recorded identity.
func settleMountAndAdapterRelease(
	ctx context.Context,
	c *Coordinator,
	req CancelReleaseRequest,
	viewID, backingDir string,
	hasIdent bool,
	ident MountIdentityRecord,
) error {
	if req.Adapter != nil {
		relRec, rErr := req.Adapter.Release(ctx, viewID)
		if rErr == nil && relRec.Settled {
			return nil
		}
		if relRec.ReasonCode != adapter.ReasonViewNotFound {
			return recordUnresolvedSettlement(c, req.WorkerID, viewID, backingDir, nil, true, nil,
				"adapter release did not settle handles or mount", rErr)
		}
	} else if req.View != nil && req.View.Core != nil {
		if err := req.View.Core.ReleaseView(); err != nil && !errors.Is(err, layeredfs.ErrViewReleased) {
			return recordUnresolvedSettlement(c, req.WorkerID, viewID, backingDir, nil, true, nil,
				"core view release failed to settle", err)
		}
	}
	if hasIdent {
		return settleRecordedMount(ctx, c, ident)
	}
	return nil
}

// recordUnresolvedSettlement keeps the identity record and adds a durable claim, so eviction,
// deletion and collection all see the custody until a retry settles it.
func recordUnresolvedSettlement(
	c *Coordinator,
	workerID, viewID, backingDir string,
	pids []int,
	mountBusy bool,
	busyPaths []string,
	reason string,
	cause error,
) error {
	claimErr := c.AcquireClaim(RetentionClaim{
		ClaimID:  "claim-unsettled-" + viewID,
		Kind:     ClaimUnresolvedSettlement,
		HolderID: viewID,
		WorkerID: workerID,
		ViewID:   viewID,
	})
	return &UnresolvedSettlementError{
		WorkerID:            workerID,
		ViewID:              viewID,
		SurvivingPIDs:       pids,
		MountBusy:           mountBusy,
		BusyMountPaths:      busyPaths,
		Reason:              reason,
		RetainedCustody:     true,
		RetainedStorageRefs: []string{backingDir},
		Cause:               errors.Join(cause, claimErr),
	}
}
