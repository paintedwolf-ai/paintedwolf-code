package recovery

import (
	"context"
	"fmt"
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
	ctx context.Context,
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
	procRec, mountRec, err = settleRecordedIdentity(ctx, c, ident)
	return procRec, mountRec, backingDir, err
}

// noteUnreleasedRuntime records that a view's namespace history is incomplete, before settling
// erases the evidence. Core persists namespace records only at a barrier and at release, so any
// made since the last barrier of a view that was never released may be lost. A retry after a
// crash during settlement still reads this.
func noteUnreleasedRuntime(c *Coordinator, j *WorkerLifecycleJournal) error {
	if j.ActiveUnknownScope {
		return nil
	}
	j.ActiveUnknownScope = true
	return c.SaveWorkerJournal(*j)
}

func salvageIfInterruptedOrUnsealed(
	c *Coordinator,
	j *WorkerLifecycleJournal,
	prevViewID, prevBacking string,
	manifest checkpoint.Manifest,
) (*InterruptedWorkingRecord, error) {
	hasUnsealed := hasUnsealedMutationsBeyondCheckpoint(prevBacking, manifest)
	if j.ActivePhase == "" && !hasUnsealed && !j.ActiveUnknownScope {
		if existing, _ := c.ListInterruptedRecords(j.WorkerID); len(existing) > 0 {
			last := existing[len(existing)-1]
			if prevViewID == "" || last.ViewID == prevViewID {
				return &last, nil
			}
		}
		return nil, nil
	}

	phase := j.ActivePhase
	if phase == "" {
		phase = PhaseActiveMutation
	}
	salvageFrom, diagnostic := prevBacking, "interrupted or unsealed state salvaged before recovery"
	if j.ActivePhase == "" && !hasUnsealed {
		// Every byte is already in the checkpoint; only the namespace history is unknown.
		salvageFrom, diagnostic = "", "view was not released; namespace changes since its last barrier are unknown"
	}
	rec, err := c.PreserveInterruptedWorkingStorage(
		j.WorkerID, prevViewID, manifest.CheckpointID, salvageFrom,
		phase, j.ActiveOperation, j.ActivePaths, j.ActiveUnknownScope,
		diagnostic,
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

	if err := recordPreparedViewIdentity(c, workerID, adp, view); err != nil {
		_, _ = adp.Release(ctx, view.ViewID)
		return nil, err
	}
	return view, nil
}

// recordPreparedViewIdentity fails rather than expose a view recovery could not later settle.
func recordPreparedViewIdentity(c *Coordinator, workerID string, adp adapter.Adapter, view *adapter.PreparedView) error {
	var mountPaths []string
	for _, r := range view.Roots {
		mountPaths = append(mountPaths, r.WorkspacePath)
	}
	connections, err := adapter.LoadRecordedConnections(view.RuntimeViewDir)
	if err != nil {
		return err
	}
	if err := c.RecordMountIdentity(MountIdentityRecord{
		ViewID:         view.ViewID,
		WorkerID:       workerID,
		AdapterID:      adp.ID(),
		Mounted:        adp.Facts().PreparationKind == adapter.PreparationMounted,
		Connections:    connections,
		MountPaths:     mountPaths,
		RuntimeViewDir: view.RuntimeViewDir,
		BackingDir:     view.BackingDir,
		BootID:         c.cfg.CurrentBootID,
	}); err != nil {
		return err
	}
	return c.UpsertInventoryEntry(InventoryEntry{
		ID:       "inv-runtime-" + view.ViewID,
		Bucket:   BucketDisposableRuntimeCache,
		WorkerID: workerID,
		ViewID:   view.ViewID,
		Path:     view.RuntimeViewDir,
		PinnedBy: []string{view.ViewID},
	})
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}
