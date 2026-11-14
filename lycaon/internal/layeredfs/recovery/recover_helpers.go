package recovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
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

// noteUnreleasedRuntime records that a view was never released, before settling erases the
// evidence. If its working storage cannot be resumed, what changed since its last checkpoint is of
// unknown scope. A retry after a crash during settlement still reads this.
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
	paths, unknownScope := j.ActivePaths, j.ActiveUnknownScope
	if logged, complete := layeredfs.WorkingLogPaths(prevBacking); complete {
		// The view's working log names every path it changed, so the scope is known.
		paths, unknownScope = append(append([]layeredfs.QualifiedPath(nil), paths...), logged...), false
		if salvageFrom == "" {
			diagnostic = "view was not released; its working log names what changed since its last barrier"
		}
	}
	rec, err := c.PreserveInterruptedWorkingStorage(
		j.WorkerID, prevViewID, manifest.CheckpointID, salvageFrom,
		phase, j.ActiveOperation, paths, unknownScope,
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
	if len(manifest.ParentCheckpoints) > 0 && len(prepReq.ParentLayers) == 0 {
		layers, _, err := c.OpenCheckpointChainLayers(ctx, manifest.ParentCheckpoints)
		if err != nil {
			return nil, err
		}
		prepReq.ParentLayers = layers
	}
	return prepareRecoveredView(ctx, c, workerID, newViewID, adp, prepReq, func(view *adapter.PreparedView) error {
		return c.ApplyCheckpointToView(ctx, view, manifest)
	})
}

// prepareRecoveredView prepares a worker's next view, binds its private Git, lets fill bring it to
// the state it resumes from, and records its identity.
func prepareRecoveredView(
	ctx context.Context,
	c *Coordinator,
	workerID, newViewID string,
	adp adapter.Adapter,
	prepReq adapter.PrepareRequest,
	fill func(*adapter.PreparedView) error,
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
	if c.cfg.GitManager != nil && prepReq.GitInterceptor == nil {
		prepReq.GitInterceptor = c.cfg.GitManager
	}
	if c.cfg.GitStore != nil && prepReq.GitObjectStore == nil {
		prepReq.GitObjectStore = c.cfg.GitStore
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
	} else if c.cfg.GitManager != nil && view.Facts.PreparationKind == adapter.PreparationMaterialized {
		if bErr := bindMaterializedViewToGitManager(ctx, c.cfg.GitManager, workerID, view); bErr != nil {
			_, _ = adp.Release(ctx, view.ViewID)
			return nil, bErr
		}
	}
	if fill != nil {
		if err := fill(view); err != nil {
			_, _ = adp.Release(ctx, view.ViewID)
			return nil, err
		}
	}

	if err := recordPreparedViewIdentity(c, workerID, adp, view); err != nil {
		_, _ = adp.Release(ctx, view.ViewID)
		return nil, err
	}
	return view, nil
}

func bindMaterializedViewToGitManager(ctx context.Context, mgr *privategit.Manager, workerID string, view *adapter.PreparedView) error {
	for _, r := range view.Roots {
		vid, wid, coreVID := view.ViewID, workerID, ""
		if r.RootID != view.PrimaryRootID {
			vid = privategit.SecondaryViewID(view.ViewID, r.RootID)
			wid = vid
			coreVID = view.ViewID
		}
		var repos []privategit.SeededRepo
		for key, seeded := range view.SeededGitRepos {
			rootID, repoRel, _ := strings.Cut(key, ":")
			if rootID == r.RootID && seeded.PrivateMetaDir != "" {
				repos = append(repos, privategit.SeededRepo{
					RepoRel:       repoRel,
					PrivateGitDir: seeded.PrivateMetaDir,
					Metrics: privategit.InitMetrics{
						ObjectsReused: seeded.ObjectsReused,
						BytesImported: seeded.BytesImported,
						BytesReused:   seeded.BytesReused,
					},
				})
			}
		}
		if err := mgr.AdoptMaterializedView(ctx, privategit.MaterializedViewBinding{
			ViewID: vid, WorkerID: wid, RootID: r.RootID, CoreViewID: coreVID,
			TrunkDir: r.SourcePath, WorktreeDir: r.WorkspacePath, Core: view.Core, Repos: repos,
		}); err != nil {
			return err
		}
	}
	return nil
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

// resumeWorkingView reopens a mounted worker's working storage after its engine exited in this
// boot, keeping every name and byte the view held. The view keeps its ID, which names its private
// Git as well. It reports false when the storage cannot be resumed: after a reboot, during another
// lifecycle operation, or when its log is unusable.
func resumeWorkingView(ctx context.Context, c *Coordinator, req RecoverRequest, j WorkerLifecycleJournal, prevViewID, prevBacking string) (*adapter.PreparedView, bool, error) {
	notResumed := func(reason string, attrs ...any) (*adapter.PreparedView, bool, error) {
		slog.InfoContext(ctx, "worker working storage not resumed", append([]any{"worker_id", req.WorkerID, "reason", reason}, attrs...)...)
		return nil, false, nil
	}
	switch {
	case prevViewID == "" || prevBacking == "":
		return notResumed("no_recorded_view")
	case j.BootID != c.cfg.CurrentBootID:
		return notResumed("other_boot")
	case j.ActivePhase != "" && j.ActivePhase != PhaseActiveMutation:
		return notResumed("lifecycle_operation", "phase", j.ActivePhase)
	case req.Adapter == nil || req.Adapter.Facts().PreparationKind != adapter.PreparationMounted:
		return notResumed("not_mounted")
	}
	prepReq := req.PrepareReq
	prepReq.BackingDir, prepReq.ResumeWorkingState = prevBacking, true
	if len(j.ParentCheckpointIDs) > 0 && len(prepReq.ParentLayers) == 0 {
		layers, _, err := c.OpenCheckpointChainLayers(ctx, j.ParentCheckpointIDs)
		if err != nil {
			return nil, false, err
		}
		prepReq.ParentLayers = layers
	}
	view, err := prepareRecoveredView(ctx, c, req.WorkerID, prevViewID, req.Adapter, prepReq, nil)
	if errors.Is(err, layeredfs.ErrWorkingStateUnusable) {
		return notResumed("working_state_unusable", "error", err)
	}
	return view, err == nil, err
}
