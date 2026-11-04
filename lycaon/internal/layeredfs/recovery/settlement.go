package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
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
// can mutate, so a later engine can settle it.
func (c *Coordinator) RecordWorkerProcess(workerID, viewID string, process adapter.ProcessIdentity) error {
	ident, ok, err := c.LoadMountIdentity(workerID, viewID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: no recorded view %s for worker %s", ErrCorruptRecoveryMetadata, viewID, workerID)
	}
	ident.RecordedProcesses = append(ident.RecordedProcesses, process)
	return c.RecordMountIdentity(ident)
}

// RecordedView describes a worker view found in durable branch and adapter records.
type RecordedView struct {
	WorkerID       string
	ViewID         string
	TaskID         string
	ProjectID      string
	AdapterID      adapter.AdapterID
	Mounted        bool
	BackingDir     string
	RuntimeViewDir string
	MountPaths     []string
	Connections    []adapter.RecordedConnection
}

// AdoptRecordedView gives a view prepared outside the coordinator a journal and identity, so
// recovery settles it by the same path as any other. Existing records keep their facts.
func (c *Coordinator) AdoptRecordedView(v RecordedView) error {
	if strings.TrimSpace(v.WorkerID) == "" || strings.TrimSpace(v.ViewID) == "" {
		return fmt.Errorf("%w: worker_id and view_id required", ErrCorruptRecoveryMetadata)
	}
	j, err := c.LoadWorkerJournal(v.WorkerID)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := c.SaveWorkerJournal(WorkerLifecycleJournal{
			WorkerID: v.WorkerID, ViewID: v.ViewID, TaskID: v.TaskID, ProjectID: v.ProjectID,
			AdapterID: v.AdapterID, BackingDir: v.BackingDir, BootID: c.cfg.CurrentBootID,
		}); err != nil {
			return err
		}
	case err != nil:
		return err
	case j.BackingDir == "" && j.ViewID == v.ViewID && v.BackingDir != "":
		j.BackingDir = v.BackingDir
		if err := c.SaveWorkerJournal(j); err != nil {
			return err
		}
	}
	ident, ok, err := c.LoadMountIdentity(v.WorkerID, v.ViewID)
	if err != nil {
		return err
	}
	if ok {
		ident.Connections = v.Connections
		ident.Mounted = ident.Mounted || v.Mounted
	} else {
		ident = MountIdentityRecord{
			ViewID: v.ViewID, WorkerID: v.WorkerID, AdapterID: v.AdapterID, Mounted: v.Mounted,
			Connections: v.Connections, MountPaths: v.MountPaths, RuntimeViewDir: v.RuntimeViewDir,
			BackingDir: v.BackingDir, BootID: c.cfg.CurrentBootID,
		}
	}
	return c.RecordMountIdentity(ident)
}

// ListWorkerIDs names every worker with a journal, including journals recovery must refuse.
func (c *Coordinator) ListWorkerIDs() ([]string, error) {
	entries, err := os.ReadDir(c.journalsDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && !strings.HasPrefix(entry.Name(), ".") {
			out = append(out, strings.TrimSuffix(entry.Name(), ".json"))
		}
	}
	return out, nil
}

// RuntimeDisposition is the outcome of settling a worker's runtime.
type RuntimeDisposition string

const (
	// RuntimeSettled means no recorded mount or writer of the worker remains.
	RuntimeSettled RuntimeDisposition = "settled"
	// RuntimeUnresolved means a mount or writer could remain; custody is retained.
	RuntimeUnresolved RuntimeDisposition = "unresolved"
	// RuntimeRefused means durable metadata is unknown or inconsistent and stays untouched.
	RuntimeRefused RuntimeDisposition = "refused"
)

// RecoveryReason is the typed cause a worker cannot resume from its recorded state.
type RecoveryReason string

const (
	ReasonRecovered            RecoveryReason = ""
	ReasonUnresolvedSettlement RecoveryReason = "unresolved_settlement"
	ReasonInvalidMetadata      RecoveryReason = "invalid_recovery_metadata"
	ReasonNoCheckpoint         RecoveryReason = "no_published_checkpoint"
	ReasonCheckpointUnusable   RecoveryReason = "checkpoint_unusable"
	ReasonSalvageFailed        RecoveryReason = "salvage_failed"
)

// WorkerRuntimeReport is what startup or pre-deletion settlement established for one worker.
type WorkerRuntimeReport struct {
	WorkerID     string
	ViewID       string
	Disposition  RuntimeDisposition
	Reason       RecoveryReason
	CheckpointID string
	Interrupted  *InterruptedWorkingRecord
	HadRuntime   bool
	Err          error
}

// Deletable reports that nothing recorded can still reach the worker's data.
func (r WorkerRuntimeReport) Deletable() bool { return r.Disposition == RuntimeSettled }

// RuntimeBlockedError carries a report that forbids deleting or evicting the worker's data.
type RuntimeBlockedError struct {
	Report WorkerRuntimeReport
}

func (e *RuntimeBlockedError) Error() string {
	return fmt.Sprintf("recovery: worker %s runtime %s (%s): %v", e.Report.WorkerID, e.Report.Disposition, e.Report.Reason, e.Report.Err)
}

func (e *RuntimeBlockedError) Unwrap() error { return e.Report.Err }

// RecoverWorkerRuntime settles a worker's recorded runtime, validates its last checkpoint and
// preserves interrupted work. It never reconstructs a view, so it is safe before deletion, and
// each step tolerates a retry after a crash in any other.
func (c *Coordinator) RecoverWorkerRuntime(ctx context.Context, workerID string) WorkerRuntimeReport {
	report := WorkerRuntimeReport{WorkerID: workerID}
	idents, err := c.ListMountIdentities(workerID)
	if err != nil {
		return report.refuse(err)
	}
	j, err := c.LoadWorkerJournal(workerID)
	if errors.Is(err, os.ErrNotExist) && len(idents) == 0 {
		report.Disposition = RuntimeSettled
		return report
	}
	if err != nil {
		return report.refuse(err)
	}
	report.ViewID, report.CheckpointID = j.ViewID, j.LastPublishedCheckpointID
	report.HadRuntime = len(idents) > 0
	backing := j.BackingDir
	for _, ident := range idents {
		if ident.ViewID == j.ViewID && ident.BackingDir != "" {
			backing = ident.BackingDir
		}
		if _, _, sErr := settleRecordedIdentity(ctx, c, ident); sErr != nil {
			report.Disposition, report.Reason, report.Err = RuntimeUnresolved, ReasonUnresolvedSettlement, sErr
			return report
		}
	}
	claims, err := c.ListClaims()
	if err != nil {
		return report.refuse(err)
	}
	for _, claim := range claims {
		if claim.Kind == ClaimUnresolvedSettlement && claim.WorkerID == workerID {
			if err := c.ReleaseClaim(claim.ClaimID); err != nil {
				report.Disposition, report.Reason, report.Err = RuntimeUnresolved, ReasonUnresolvedSettlement, err
				return report
			}
		}
	}
	manifest, checkpointErr := c.validatedLastCheckpoint(ctx, j)
	interrupted, err := salvageIfInterruptedOrUnsealed(c, &j, j.ViewID, backing, manifest)
	if err != nil {
		report.Disposition, report.Reason, report.Err = RuntimeUnresolved, ReasonSalvageFailed, err
		return report
	}
	report.Interrupted = interrupted
	j.ActivePhase, j.ActiveOperation, j.ActivePaths, j.ActiveUnknownScope = "", "", nil, false
	if err := c.SaveWorkerJournal(j); err != nil {
		report.Disposition, report.Reason, report.Err = RuntimeUnresolved, ReasonSalvageFailed, err
		return report
	}
	report.Disposition = RuntimeSettled
	if j.LastPublishedCheckpointID == "" {
		report.Reason = ReasonNoCheckpoint
	} else if checkpointErr != nil {
		report.Reason, report.Err = ReasonCheckpointUnusable, checkpointErr
	}
	return report
}

func (c *Coordinator) validatedLastCheckpoint(ctx context.Context, j WorkerLifecycleJournal) (checkpoint.Manifest, error) {
	if j.LastPublishedCheckpointID == "" {
		return checkpoint.Manifest{}, nil
	}
	return c.VerifyCheckpointReconstructible(ctx, j.LastPublishedCheckpointID)
}

func (r WorkerRuntimeReport) refuse(err error) WorkerRuntimeReport {
	r.Disposition, r.Reason, r.Err = RuntimeRefused, ReasonInvalidMetadata, err
	return r
}

// ReleaseWorkerResult drops the retention of a worker whose result has been promoted or
// discarded, after its runtime settled. Collection then removes only what no other worker,
// stacked child or unresolved custody still references.
func (c *Coordinator) ReleaseWorkerResult(workerID string) error {
	j, err := c.LoadWorkerJournal(workerID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	idents, err := c.ListMountIdentities(workerID)
	if err != nil {
		return err
	}
	if len(idents) > 0 {
		return fmt.Errorf("%w: worker %s still has %d recorded views", ErrUnresolvedSettlement, workerID, len(idents))
	}
	claims, err := c.ListClaims()
	if err != nil {
		return err
	}
	var errs []error
	for _, claim := range claims {
		if claim.WorkerID == workerID && (claim.Kind == ClaimCheckpoint || claim.Kind == ClaimActiveView) {
			errs = append(errs, c.ReleaseClaim(claim.ClaimID))
		}
	}
	if j.BackingDir != "" {
		errs = append(errs, os.RemoveAll(j.BackingDir))
	}
	errs = append(errs, c.removeWorkerJournal(workerID))
	return errors.Join(errs...)
}

func (c *Coordinator) removeWorkerJournal(workerID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := os.Remove(filepath.Join(c.journalsDir, workerID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
