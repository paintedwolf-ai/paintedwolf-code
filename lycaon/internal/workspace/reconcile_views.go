package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
)

const adapterRuntimeDirName = ".adapter"

// discoveredView joins a recorded view to the branch it serves, when one exists.
type discoveredView struct {
	view recovery.RecordedView
	root string
}

// discoverRecordedViews reads durable job metadata and adapter connection records. Every
// result names one recorded identity; nothing is inferred from mount paths or the kernel.
func discoverRecordedViews(ctx context.Context, branchRoot string) ([]discoveredView, []recovery.WorkerRuntimeReport, error) {
	trees, err := ListJobTrees(ctx, branchRoot)
	if err != nil {
		return nil, nil, err
	}
	var views []discoveredView
	var refused []recovery.WorkerRuntimeReport
	seen := make(map[string]struct{})
	for _, tree := range trees {
		view, ok, err := recordedViewForBranch(branchRoot, tree.JobID, tree.Root)
		if err != nil {
			refused = append(refused, recovery.WorkerRuntimeReport{
				WorkerID: tree.JobID, Disposition: recovery.RuntimeRefused, Reason: recovery.ReasonInvalidMetadata, Err: err,
			})
			seen[tree.JobID] = struct{}{}
			continue
		}
		if ok {
			views = append(views, discoveredView{view: view, root: tree.Root})
			seen[view.ViewID] = struct{}{}
		}
	}
	orphans, orphanRefused, err := discoverOrphanConnections(branchRoot, seen)
	if err != nil {
		return nil, nil, err
	}
	return append(views, orphans...), append(refused, orphanRefused...), nil
}

// discoverOrphanConnections finds mounts recorded before their job metadata completed. The view
// id doubles as the worker id, as the claim path assigns it.
func discoverOrphanConnections(branchRoot string, seen map[string]struct{}) ([]discoveredView, []recovery.WorkerRuntimeReport, error) {
	runtimeBase := filepath.Join(branchRoot, adapterRuntimeDirName)
	projects, err := os.ReadDir(runtimeBase)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var views []discoveredView
	var refused []recovery.WorkerRuntimeReport
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(runtimeBase, project.Name()))
		if err != nil {
			return nil, nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, known := seen[entry.Name()]; known {
				continue
			}
			runtimeViewDir := filepath.Join(runtimeBase, project.Name(), entry.Name())
			connections, err := adapter.LoadRecordedConnections(runtimeViewDir)
			if err != nil {
				refused = append(refused, recovery.WorkerRuntimeReport{
					WorkerID: entry.Name(), Disposition: recovery.RuntimeRefused, Reason: recovery.ReasonInvalidMetadata, Err: err,
				})
				continue
			}
			if len(connections) == 0 {
				continue
			}
			views = append(views, discoveredView{view: recovery.RecordedView{
				WorkerID: entry.Name(), ViewID: entry.Name(), TaskID: entry.Name(), ProjectID: project.Name(),
				AdapterID: connections[0].AdapterID, Mounted: true, RuntimeViewDir: runtimeViewDir,
				MountPaths: connectionMountPaths(connections), Connections: connections,
			}})
		}
	}
	return views, refused, nil
}

func connectionMountPaths(connections []adapter.RecordedConnection) []string {
	paths := make([]string, 0, len(connections))
	for _, connection := range connections {
		paths = append(paths, connection.MountPath)
	}
	return paths
}

// recordedViewForBranch reports a branch's mounted view from its job metadata. Materialized
// trees need no settlement: nothing but their own tree holds their state.
func recordedViewForBranch(branchRoot, jobID, root string) (recovery.RecordedView, bool, error) {
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(root))
	if errors.Is(err, os.ErrNotExist) {
		return recovery.RecordedView{}, false, nil
	}
	if err != nil {
		return recovery.RecordedView{}, false, err
	}
	if meta.PreparationKind != adapter.PreparationMounted || strings.TrimSpace(meta.ViewID) == "" {
		return recovery.RecordedView{}, false, nil
	}
	projectKey := filepath.Base(filepath.Dir(root))
	runtimeViewDir := filepath.Join(branchRoot, adapterRuntimeDirName, projectKey, meta.ViewID)
	connections, err := adapter.LoadRecordedConnections(runtimeViewDir)
	if err != nil {
		return recovery.RecordedView{}, false, err
	}
	backing := ""
	if journal := strings.TrimSpace(meta.JournalPath); journal != "" {
		backing = filepath.Dir(journal)
	}
	mountPaths := []string{root}
	if len(connections) > 0 {
		mountPaths = connectionMountPaths(connections)
	}
	return recovery.RecordedView{
		WorkerID: jobID, ViewID: meta.ViewID, TaskID: jobID, ProjectID: projectKey,
		AdapterID: meta.AdapterID, Mounted: true, BackingDir: backing, RuntimeViewDir: runtimeViewDir,
		MountPaths: mountPaths, Connections: connections,
	}, true, nil
}

func (m *Manager) recoveryCoordinator() *recovery.Coordinator {
	if m.recovery != nil {
		return m.recovery
	}
	if services, err := m.layered(); err == nil && services != nil {
		return services.recovery
	}
	return nil
}

func (m *Manager) recoveryCoordinatorForJob(jobID string) (*recovery.Coordinator, recovery.WorkerTransitionJournal, error) {
	coord := m.recoveryCoordinator()
	if coord == nil {
		return nil, recovery.WorkerTransitionJournal{}, os.ErrNotExist
	}
	journal, err := coord.LoadWorkerJournal(jobID)
	if errors.Is(err, os.ErrNotExist) && m.recovery != nil {
		if services, sErr := m.layered(); sErr == nil && services != nil && services.recovery != nil && services.recovery != coord {
			if j2, err2 := services.recovery.LoadWorkerJournal(jobID); err2 == nil {
				return services.recovery, j2, nil
			}
		}
	}
	return coord, journal, err
}

// RecoverWorkerViews runs once at engine startup, before any worker can run: it settles every
// recorded mount and writer left by an earlier process, preserves interrupted work and marks
// each lost mounted tree for rebuild. One stuck worker never blocks the others or the engine.
func (m *Manager) RecoverWorkerViews(ctx context.Context) ([]recovery.WorkerRuntimeReport, error) {
	coord := m.recoveryCoordinator()
	if coord == nil {
		return nil, nil
	}
	discovered, reports, err := discoverRecordedViews(ctx, m.branchRoot)
	if err != nil {
		return nil, err
	}
	refused := make(map[string]struct{}, len(reports))
	for _, report := range reports {
		refused[report.WorkerID] = struct{}{}
	}
	roots := make(map[string]string, len(discovered))
	for _, found := range discovered {
		if err := coord.AdoptRecordedView(found.view); err != nil {
			reports = append(reports, recovery.WorkerRuntimeReport{
				WorkerID: found.view.WorkerID, ViewID: found.view.ViewID, Disposition: recovery.RuntimeRefused,
				Reason: recovery.ReasonInvalidMetadata, Err: err,
			})
			refused[found.view.WorkerID] = struct{}{}
			continue
		}
		if found.root != "" {
			roots[found.view.WorkerID] = found.root
		}
	}
	workerIDs, err := coord.ListWorkerIDs()
	if err != nil {
		return reports, err
	}
	for _, workerID := range workerIDs {
		if _, skip := refused[workerID]; skip {
			continue
		}
		report := coord.RecoverWorkerRuntime(ctx, workerID)
		if report.Deletable() && report.HadRuntime {
			if root, ok := roots[workerID]; ok {
				if err := markBranchNeedsRebuild(root); err != nil {
					report.Disposition, report.Reason, report.Err = recovery.RuntimeUnresolved, recovery.ReasonSalvageFailed, err
				}
			}
		}
		logWorkerRecovery(ctx, report)
		reports = append(reports, report)
	}
	return reports, m.offerMaterializedBranchesForRecovery(ctx)
}

func logWorkerRecovery(ctx context.Context, report recovery.WorkerRuntimeReport) {
	if report.Deletable() && report.Err == nil {
		slog.InfoContext(ctx, "settled worker view at startup", "worker_id", report.WorkerID, "view_id", report.ViewID,
			"checkpoint_id", report.CheckpointID, "preserved_interrupted_work", report.Interrupted != nil)
		return
	}
	slog.WarnContext(ctx, "worker view recovery retained custody", "worker_id", report.WorkerID, "view_id", report.ViewID,
		"disposition", report.Disposition, "reason", report.Reason, "err", report.Err)
}

// markBranchNeedsRebuild records that a mounted tree's content left with its mount.
func markBranchNeedsRebuild(root string) error {
	metaDir := enginepaths.MetaDirForBranchRoot(root)
	meta, err := LoadJobMeta(metaDir)
	if err != nil {
		return err
	}
	if !meta.SnapshotComplete {
		return nil
	}
	meta.SnapshotComplete = false
	return WriteJobMeta(metaDir, meta)
}

// SettleBranchRuntime proves a worker's runtime is gone before its data may be deleted. A view
// this process serves releases through its adapter; anything else settles by recorded identity.
func (m *Manager) SettleBranchRuntime(ctx context.Context, jobID, root string) error {
	if m.recovery == nil {
		return nil
	}
	if isMaterializedBranch(root) {
		return m.settleMaterializedRuntime(ctx, root)
	}
	view, recorded, err := recordedViewForBranch(m.branchRoot, jobID, root)
	if err != nil {
		return &recovery.RuntimeBlockedError{Report: recovery.WorkerRuntimeReport{
			WorkerID: jobID, Disposition: recovery.RuntimeRefused, Reason: recovery.ReasonInvalidMetadata, Err: err,
		}}
	}
	if recorded {
		if err := m.recovery.AdoptRecordedView(view); err != nil {
			return &recovery.RuntimeBlockedError{Report: recovery.WorkerRuntimeReport{
				WorkerID: jobID, ViewID: view.ViewID, Disposition: recovery.RuntimeRefused, Reason: recovery.ReasonInvalidMetadata, Err: err,
			}}
		}
	}
	if err := m.releaseLiveJournaledViews(ctx, jobID, root); err != nil {
		return err
	}
	report := m.recovery.RecoverWorkerRuntime(ctx, jobID)
	if !report.Deletable() {
		return &recovery.RuntimeBlockedError{Report: report}
	}
	return nil
}

func (m *Manager) releaseLiveJournaledViews(ctx context.Context, jobID, root string) error {
	if _, err := m.recovery.LoadWorkerJournal(jobID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return &recovery.RuntimeBlockedError{Report: recovery.WorkerRuntimeReport{
			WorkerID: jobID, Disposition: recovery.RuntimeRefused, Reason: recovery.ReasonInvalidMetadata, Err: err,
		}}
	}
	key := filepath.Clean(root)
	m.viewsMu.Lock()
	state, live := m.views[key]
	m.viewsMu.Unlock()
	if !live || state.selection.Adapter == nil {
		return nil
	}
	for _, view := range state.views {
		if view == nil {
			continue
		}
		if _, err := m.recovery.CancelAndRelease(ctx, recovery.CancelReleaseRequest{
			WorkerID: jobID, ViewID: view.ViewID, View: view, Adapter: state.selection.Adapter,
		}); err != nil {
			return err
		}
	}
	m.viewsMu.Lock()
	delete(m.views, key)
	m.viewsMu.Unlock()
	sharedBranchViews.Delete(key)
	forgetSettledBranchClaim(key)
	return nil
}

// RebuildRecoveredBranch reconstructs a branch from its worker's last published checkpoint on
// whichever adapter selection now chooses. It reports false when no checkpoint could apply, so
// the caller's other rebuild source still gets its turn.
func (m *Manager) RebuildRecoveredBranch(ctx context.Context, root, jobID string) (bool, error) {
	coord, journal, err := m.recoveryCoordinatorForJob(jobID)
	if errors.Is(err, os.ErrNotExist) || coord == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if journal.LastPublishedCheckpointID == "" {
		return false, nil
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(root))
	if err != nil {
		return false, err
	}
	layout, err := layoutFromJobMeta(meta)
	if err != nil {
		return false, err
	}
	grants := grantsForSources(layout.Roots)
	projectKey := filepath.Base(filepath.Dir(root))
	var overrun *adapter.BudgetOverrunError
	for {
		choice, err := m.chooseWorkspaceAdapter(ctx, root, grants, journal.ProjectID, overrun)
		if err != nil {
			return false, err
		}
		viewID := fmt.Sprintf("%s-%s", jobID, uuid.NewString()[:8])
		runtimeDir := filepath.Join(m.branchRoot, adapterRuntimeDirName, projectKey)
		req := adapter.PrepareRequest{
			ViewID: viewID, RuntimeDir: runtimeDir, BackingDir: filepath.Join(runtimeDir, viewID+".backing"),
			PristineDir: enginepaths.PristineTreeForBranchRoot(root), StatManifestPath: enginepaths.StatManifestForBranchRoot(root),
			CopyBudget: choice.CopyBudget, SelectionFacts: choice.Selection, CloneFile: m.cloneFile,
		}
		result, _, err := m.RebuildWorkerWorkspaceFromCheckpoint(ctx, root, coord, recovery.RecoverRequest{
			WorkerID: jobID, ExpectedTaskID: journal.TaskID, ExpectedProjectID: journal.ProjectID, NewViewID: viewID,
			Adapter: choice.Adapter, PrepareReq: req,
		})
		if errors.As(err, &overrun) && choice.CopyBudget != nil {
			continue
		}
		if err != nil {
			return false, err
		}
		if result == nil || result.PreparedView == nil {
			return true, nil
		}
		m.recordBranchViews(root, choice, result.PreparedView)
		if err := markSnapshotCompleteWithClaim(root, choice, result.PreparedView, nil, nil); err != nil {
			return false, err
		}
		if err := recordBranchCheckpoint(root, coord.CheckpointStore(), journal.LastPublishedCheckpointID); err != nil {
			return false, err
		}
		if choice.Facts.PreparationKind == adapter.PreparationMounted {
			if err := m.adoptRecoveredMountedView(ctx, root, jobID, journal.ProjectID, choice.Adapter, result.PreparedView); err != nil {
				return false, err
			}
		} else if err := m.adoptRecoveredMaterializedView(ctx, root, jobID, journal.ProjectID, choice.Adapter, result.PreparedView); err != nil {
			return false, err
		}
		return true, nil
	}
}
