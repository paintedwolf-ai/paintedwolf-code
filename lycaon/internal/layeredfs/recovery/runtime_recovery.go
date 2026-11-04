package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

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
	c.identityMu.Lock()
	defer c.identityMu.Unlock()
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

// AdoptMaterializedView gives a materialized worker a journal, so its checkpoints have a record and
// retention follows its rules. It records no mount identity: the tree alone holds a materialized
// view's state, so there is nothing to settle before a restart or a deletion.
func (c *Coordinator) AdoptMaterializedView(v RecordedView) error {
	if strings.TrimSpace(v.WorkerID) == "" || strings.TrimSpace(v.ViewID) == "" {
		return fmt.Errorf("%w: worker_id and view_id required", ErrCorruptRecoveryMetadata)
	}
	c.identityMu.Lock()
	defer c.identityMu.Unlock()
	j, err := c.LoadWorkerJournal(v.WorkerID)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return c.SaveWorkerJournal(WorkerLifecycleJournal{
			WorkerID: v.WorkerID, ViewID: v.ViewID, TaskID: v.TaskID, ProjectID: v.ProjectID,
			AdapterID: v.AdapterID, BootID: c.cfg.CurrentBootID,
		})
	case err != nil:
		return err
	case j.ViewID != v.ViewID:
		j.ViewID, j.AdapterID, j.BootID = v.ViewID, v.AdapterID, c.cfg.CurrentBootID
		return c.SaveWorkerJournal(j)
	}
	return nil
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
	if report.HadRuntime {
		if err := noteUnreleasedRuntime(c, &j); err != nil {
			report.Disposition, report.Reason, report.Err = RuntimeUnresolved, ReasonSalvageFailed, err
			return report
		}
	}
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
