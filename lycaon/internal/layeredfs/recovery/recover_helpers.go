package recovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

func validateWorkerOwnership(j WorkerLifecycleJournal, expectedTaskID, expectedProjectID string) error {
	if expectedTaskID != "" && j.TaskID != expectedTaskID {
		return fmt.Errorf("%w: task_id got %q want %q", ErrWorkerIdentityMismatch, j.TaskID, expectedTaskID)
	}
	if expectedProjectID != "" && j.ProjectID != expectedProjectID {
		return fmt.Errorf("%w: project_id got %q want %q", ErrWorkerIdentityMismatch, j.ProjectID, expectedProjectID)
	}
	return nil
}

func settleOwnedRuntimeForRecovery(
	c *Coordinator,
	workerID, prevViewID, fallbackBacking string,
) (ProcessSettlementReceipt, MountSettlementReceipt, string, error) {
	var procRec ProcessSettlementReceipt
	var mountRec MountSettlementReceipt
	backingDir := fallbackBacking

	if prevViewID == "" {
		return procRec, mountRec, backingDir, nil
	}
	ident, ok, err := c.LoadMountIdentity(workerID, prevViewID)
	if err != nil {
		return procRec, mountRec, backingDir, err
	}
	if !ok {
		return procRec, mountRec, backingDir, nil
	}
	if backingDir == "" {
		backingDir = ident.BackingDir
	}

	if c.cfg.ProcessCtrl != nil && len(ident.RecordedPIDs) > 0 {
		procRec, err = c.cfg.ProcessCtrl.SettleProcesses(workerID, prevViewID, ident.RecordedPIDs)
		if err != nil || !procRec.Settled || len(procRec.SurvivingPIDs) > 0 {
			uErr := &UnresolvedSettlementError{
				WorkerID:            workerID,
				ViewID:              prevViewID,
				SurvivingPIDs:       procRec.SurvivingPIDs,
				Reason:              "surviving worker writer processes could not be settled before recovery",
				RetainedCustody:     true,
				RetainedStorageRefs: []string{ident.BackingDir, ident.RuntimeViewDir},
				Cause:               err,
			}
			_ = c.AcquireClaim(RetentionClaim{
				ClaimID:  "claim-unsettled-" + prevViewID,
				Kind:     ClaimUnresolvedSettlement,
				HolderID: prevViewID,
				WorkerID: workerID,
				ViewID:   prevViewID,
			})
			return procRec, mountRec, backingDir, uErr
		}
	}

	if c.cfg.MountReconciler != nil {
		mountRec, err = c.cfg.MountReconciler.ReconcileWorkerMount(ident)
		if err != nil || !mountRec.Settled || len(mountRec.BusyPaths) > 0 {
			uErr := &UnresolvedSettlementError{
				WorkerID:            workerID,
				ViewID:              prevViewID,
				MountBusy:           true,
				BusyMountPaths:      mountRec.BusyPaths,
				Reason:              "owned worker mount remained busy during recovery reconciliation",
				RetainedCustody:     true,
				RetainedStorageRefs: []string{ident.BackingDir, ident.RuntimeViewDir},
				Cause:               err,
			}
			_ = c.AcquireClaim(RetentionClaim{
				ClaimID:  "claim-unsettled-" + prevViewID,
				Kind:     ClaimUnresolvedSettlement,
				HolderID: prevViewID,
				WorkerID: workerID,
				ViewID:   prevViewID,
			})
			return procRec, mountRec, backingDir, uErr
		}
	}

	_ = c.ReleaseClaim("claim-unsettled-" + prevViewID)
	_ = c.RemoveMountIdentity(workerID, prevViewID)
	_ = c.RemoveInventoryEntry("inv-runtime-" + prevViewID)
	return procRec, mountRec, backingDir, nil
}

func salvageIfInterruptedOrUnsealed(
	c *Coordinator,
	j *WorkerLifecycleJournal,
	prevViewID, prevBacking string,
	manifest checkpoint.Manifest,
) (*InterruptedWorkingRecord, error) {
	hasTmpManifest := hasUncommittedCheckpointManifest(c)
	hasUnsealed := hasUnsealedMutationsBeyondCheckpoint(prevBacking, manifest)
	if j.ActivePhase == "" && !hasTmpManifest && !hasUnsealed {
		if existing, _ := c.ListInterruptedRecords(j.WorkerID); len(existing) > 0 {
			last := existing[len(existing)-1]
			if prevViewID == "" || last.ViewID == prevViewID {
				return &last, nil
			}
		}
		return nil, nil
	}

	phase := j.ActivePhase
	if phase == "" && hasTmpManifest {
		phase = PhasePublication
	}
	if phase == "" {
		phase = PhaseActiveMutation
	}
	rec, err := c.PreserveInterruptedWorkingStorage(
		j.WorkerID, prevViewID, manifest.CheckpointID, prevBacking,
		phase, j.ActiveOperation, j.ActivePaths, j.ActiveUnknownScope,
		"interrupted or unsealed state salvaged before recovery",
	)
	if err != nil {
		return nil, err
	}
	j.Transitions = append(j.Transitions, LifecycleTransitionRecord{
		Seq:          uint64(len(j.Transitions) + 1),
		Phase:        phase,
		State:        StateInterrupted,
		ViewID:       prevViewID,
		CheckpointID: manifest.CheckpointID,
		Operation:    j.ActiveOperation,
		Paths:        rec.AffectedPaths,
		UnknownScope: rec.UnknownScope,
		Diagnostic:   "salvaged to " + rec.QuarantineDir,
		RecordedAt:   time.Now().UTC(),
	})
	return rec, nil
}

func hasUncommittedCheckpointManifest(c *Coordinator) bool {
	cpRoot := filepath.Join(c.cfg.CheckpointStore.RootDir(), "checkpoints")
	entries, err := os.ReadDir(cpRoot)
	if err != nil {
		return false
	}
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		tmp := filepath.Join(cpRoot, d.Name(), "manifest.json.tmp")
		fin := filepath.Join(cpRoot, d.Name(), "manifest.json")
		if _, tErr := os.Stat(tmp); tErr == nil {
			if _, fErr := os.Stat(fin); fErr != nil {
				return true
			}
		}
	}
	return false
}

func reconstructViewFromManifest(
	ctx context.Context,
	c *Coordinator,
	workerID, newViewID string,
	adp adapter.Adapter,
	prepReq adapter.PrepareRequest,
	manifest checkpoint.Manifest,
) (*adapter.PreparedView, error) {
	if adp == nil {
		return nil, fmt.Errorf("recovery: Adapter is required")
	}
	if newViewID != "" {
		prepReq.ViewID = newViewID
	}
	if strings.TrimSpace(prepReq.ViewID) == "" {
		prepReq.ViewID = fmt.Sprintf("view-%s-rec-%d", workerID, c.seq.Add(1))
	}
	if len(manifest.ParentCheckpoints) > 0 && len(prepReq.ParentLayers) == 0 {
		layers, _, err := c.OpenCheckpointChainLayers(ctx, manifest.ParentCheckpoints)
		if err != nil {
			return nil, err
		}
		prepReq.ParentLayers = layers
	}
	if c.cfg.GitManager != nil && prepReq.GitInterceptor == nil {
		prepReq.GitInterceptor = c.cfg.GitManager
	}

	view, err := adp.Prepare(ctx, prepReq)
	if err != nil {
		return nil, err
	}
	if binder, ok := adp.(PrivateGitBinder); ok && c.cfg.GitManager != nil {
		if bErr := binder.BindPrivateGit(view.ViewID, workerID, c.cfg.GitManager); bErr != nil {
			_, _ = adp.Release(ctx, view.ViewID)
			return nil, bErr
		}
	}
	if err := c.ApplyCheckpointToView(ctx, view, manifest); err != nil {
		_, _ = adp.Release(ctx, view.ViewID)
		return nil, err
	}

	var mountPaths []string
	for _, r := range view.Roots {
		mountPaths = append(mountPaths, r.WorkspacePath)
	}
	_ = c.RecordMountIdentity(MountIdentityRecord{
		ViewID:         view.ViewID,
		WorkerID:       workerID,
		AdapterID:      adp.ID(),
		MountPaths:     mountPaths,
		RuntimeViewDir: view.RuntimeViewDir,
		BackingDir:     view.BackingDir,
		BootID:         c.cfg.CurrentBootID,
	})
	_ = c.UpsertInventoryEntry(InventoryEntry{
		ID:       "inv-runtime-" + view.ViewID,
		Bucket:   BucketDisposableRuntimeCache,
		WorkerID: workerID,
		ViewID:   view.ViewID,
		Path:     view.RuntimeViewDir,
		PinnedBy: []string{view.ViewID},
	})
	return view, nil
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}
