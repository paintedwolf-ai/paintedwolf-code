package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

const (
	processStopBound    = 10 * time.Second
	processStopInterval = 50 * time.Millisecond
)

// settleRecordedIdentity stops a view's recorded writers, then settles its connection, and only
// then forgets the identity. Any failure keeps the record, so a retry resumes from the same facts.
func settleRecordedIdentity(ctx context.Context, c *Coordinator, ident MountIdentityRecord) (ProcessSettlementReceipt, MountSettlementReceipt, error) {
	procRec, err := settleRecordedProcesses(ctx, c, ident)
	if err != nil {
		return procRec, MountSettlementReceipt{}, err
	}
	mountRec, err := reconcileRecordedMount(ctx, c, ident)
	if err != nil {
		return procRec, mountRec, err
	}
	return procRec, mountRec, forgetSettledView(c, ident.WorkerID, ident.ViewID)
}

func settleRecordedProcesses(ctx context.Context, c *Coordinator, ident MountIdentityRecord) (ProcessSettlementReceipt, error) {
	if c.cfg.ProcessCtrl == nil || len(ident.RecordedProcesses) == 0 {
		return ProcessSettlementReceipt{WorkerID: ident.WorkerID, ViewID: ident.ViewID, Settled: true}, nil
	}
	rec, err := c.cfg.ProcessCtrl.SettleProcesses(ctx, ident.WorkerID, ident.ViewID, ident.RecordedProcesses)
	if err != nil || !rec.Settled || len(rec.SurvivingPIDs) > 0 {
		return rec, recordUnresolvedSettlement(c, ident.WorkerID, ident.ViewID, ident.BackingDir, rec.SurvivingPIDs, false, nil,
			"surviving worker writer processes could not be settled", err)
	}
	return rec, nil
}

func settleRecordedMount(ctx context.Context, c *Coordinator, ident MountIdentityRecord) error {
	_, err := reconcileRecordedMount(ctx, c, ident)
	return err
}

func reconcileRecordedMount(ctx context.Context, c *Coordinator, ident MountIdentityRecord) (MountSettlementReceipt, error) {
	if c.cfg.MountReconciler == nil {
		if !ident.Mounted {
			return MountSettlementReceipt{ViewID: ident.ViewID, Settled: true}, nil
		}
		return MountSettlementReceipt{ViewID: ident.ViewID}, recordUnresolvedSettlement(c, ident.WorkerID, ident.ViewID, ident.BackingDir, nil, true, ident.MountPaths,
			"no mount reconciler can settle this mounted view", nil)
	}
	rec, err := c.cfg.MountReconciler.ReconcileWorkerMount(ctx, ident)
	if err != nil || !rec.Settled || len(rec.BusyPaths) > 0 {
		return rec, recordUnresolvedSettlement(c, ident.WorkerID, ident.ViewID, ident.BackingDir, nil, true, rec.BusyPaths,
			"owned worker mount did not settle", err)
	}
	return rec, nil
}

func forgetSettledView(c *Coordinator, workerID, viewID string) error {
	return errors.Join(
		c.ReleaseClaim("claim-unsettled-"+viewID),
		c.RemoveMountIdentity(workerID, viewID),
		c.RemoveInventoryEntry("inv-runtime-"+viewID),
	)
}

// ConnectionReconciler settles recorded mounts through the adapter contract. A mounted adapter
// with no recovery path settles only when no mount boundary remains at its recorded paths.
type ConnectionReconciler struct {
	Recoveries map[adapter.AdapterID]adapter.ConnectionRecovery
}

// ReconcileWorkerMount settles each recorded connection of rec.
func (r ConnectionReconciler) ReconcileWorkerMount(ctx context.Context, rec MountIdentityRecord) (MountSettlementReceipt, error) {
	receipt := MountSettlementReceipt{ViewID: rec.ViewID}
	recovery, known := r.Recoveries[rec.AdapterID]
	if !known {
		if err := collectMountBoundaries(rec.MountPaths, &receipt); err != nil {
			return receipt, err
		}
		receipt.Settled = len(receipt.BusyPaths) == 0
		return receipt, nil
	}
	var errs []error
	for _, connection := range rec.Connections {
		settlement, err := recovery.SettleRecordedConnection(ctx, connection)
		receipt.ConnectionAborted = receipt.ConnectionAborted || settlement.ConnectionAborted
		switch {
		case err != nil:
			errs = append(errs, err)
			receipt.BusyPaths = append(receipt.BusyPaths, connection.MountPath)
		case settlement.Settled():
			receipt.UnmountedPaths = append(receipt.UnmountedPaths, connection.MountPath)
		default:
			errs = append(errs, fmt.Errorf("connection %s: %s: %s", connection.MountPath, settlement.Disposition, settlement.Diagnostic))
			receipt.BusyPaths = append(receipt.BusyPaths, connection.MountPath)
		}
	}
	receipt.Settled = len(receipt.BusyPaths) == 0
	return receipt, errors.Join(errs...)
}

func collectMountBoundaries(paths []string, receipt *MountSettlementReceipt) error {
	for _, path := range paths {
		mounted, err := adapter.MountBoundaryPresent(path)
		if err != nil {
			return err
		}
		if mounted {
			receipt.BusyPaths = append(receipt.BusyPaths, path)
		}
	}
	return nil
}

// RecordedProcessController stops only processes whose recorded incarnation still runs, and
// never touches a PID another process has since reused.
type RecordedProcessController struct{}

// SettleProcesses signals the recorded incarnations and waits, bounded, for proof they ended.
func (RecordedProcessController) SettleProcesses(ctx context.Context, workerID, viewID string, recorded []adapter.ProcessIdentity) (ProcessSettlementReceipt, error) {
	receipt := ProcessSettlementReceipt{WorkerID: workerID, ViewID: viewID}
	for _, process := range recorded {
		if process.Ended() {
			continue
		}
		if err := killRecordedProcess(process); err != nil {
			receipt.SurvivingPIDs = append(receipt.SurvivingPIDs, process.PID)
			return receipt, fmt.Errorf("stop recorded process %d: %w", process.PID, err)
		}
		receipt.StoppedPIDs = append(receipt.StoppedPIDs, process.PID)
	}
	deadline := time.Now().Add(processStopBound)
	for {
		receipt.SurvivingPIDs = receipt.SurvivingPIDs[:0]
		for _, process := range recorded {
			if !process.Ended() {
				receipt.SurvivingPIDs = append(receipt.SurvivingPIDs, process.PID)
			}
		}
		if len(receipt.SurvivingPIDs) == 0 {
			receipt.Settled = true
			return receipt, nil
		}
		if time.Now().After(deadline) {
			return receipt, nil
		}
		select {
		case <-ctx.Done():
			return receipt, ctx.Err()
		case <-time.After(processStopInterval):
		}
	}
}

func killRecordedProcess(process adapter.ProcessIdentity) error {
	handle, err := os.FindProcess(process.PID)
	if err != nil {
		return err
	}
	if err := handle.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// RecordWorkerProcess adds a started writer's identity to its view's record before the process
// can mutate, so a later engine can settle it. Writers that ended leave the record, which keeps it
// to the view's live processes however many commands the worker runs.
func (c *Coordinator) RecordWorkerProcess(workerID, viewID string, process adapter.ProcessIdentity) error {
	c.identityMu.Lock()
	defer c.identityMu.Unlock()
	ident, ok, err := c.LoadMountIdentity(workerID, viewID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: no recorded view %s for worker %s", ErrCorruptRecoveryMetadata, viewID, workerID)
	}
	live := ident.RecordedProcesses[:0]
	for _, recorded := range ident.RecordedProcesses {
		if !recorded.Ended() {
			live = append(live, recorded)
		}
	}
	ident.RecordedProcesses = append(live, process)
	return c.RecordMountIdentity(ident)
}

// SettleRecordedWriters stops the recorded writers of a view this engine is about to release.
func (c *Coordinator) SettleRecordedWriters(ctx context.Context, workerID, viewID string) error {
	ident, ok, err := c.LoadMountIdentity(workerID, viewID)
	if err != nil || !ok {
		return err
	}
	_, err = settleRecordedProcesses(ctx, c, ident)
	return err
}

// ForgetReleasedView drops the records of a view whose adapter already released it.
func (c *Coordinator) ForgetReleasedView(workerID, viewID string) error {
	return forgetSettledView(c, workerID, viewID)
}
