package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// BranchUsesCheckpoints reports that a branch's result is captured by checkpoint, so its claim records
// no baseline: a mounted view from its journal, a materialized one from its stat manifest. A branch
// selected by this rule says so in its metadata; one from an earlier format keeps its baseline.
func BranchUsesCheckpoints(branchRoot string) bool {
	if adapter.HasStatManifest(enginepaths.StatManifestForBranchRoot(branchRoot)) {
		return true
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot))
	return err == nil && (meta.PreparationKind == adapter.PreparationMounted || meta.SelectionReason != "")
}

// materializedBranchLostItsView reports a materialized branch with no manifest to capture through,
// as a restored backup leaves one: its metadata comes back without its base.
func materializedBranchLostItsView(branchRoot string) bool {
	if adapter.HasStatManifest(enginepaths.StatManifestForBranchRoot(branchRoot)) {
		return true
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot))
	return err == nil && meta.PreparationKind == adapter.PreparationMaterialized && meta.SelectionReason != ""
}

func branchTargetDirs(branchRoot string, grants []adapter.RootGrant) (map[string]string, error) {
	targets := make(map[string]string, len(grants))
	for _, g := range grants {
		if len(grants) == 1 {
			targets[g.RootID] = branchRoot
			continue
		}
		dir, err := projectroot.BranchDirForID(g.RootID)
		if err != nil {
			return nil, err
		}
		targets[g.RootID] = filepath.Join(branchRoot, dir)
	}
	return targets, nil
}

func primarySourcePath(sources []projectroot.RootRef) string {
	for _, s := range sources {
		if s.IsPrimary {
			return s.Path
		}
	}
	return sources[0].Path
}

func preparationStrategy(facts adapter.SelectionFacts) ProvisionStrategy {
	if facts.CloneAvailable {
		return ProvisionDirectCoW
	}
	return ProvisionDirectCopy
}

// prepareMaterializedWorkspaceView claims the worker's one view across its roots. The cost is declared
// before the first file moves, and cancellation is honored throughout.
func (m *Manager) prepareMaterializedWorkspaceView(ctx context.Context, binding *Binding, sources []projectroot.RootRef, choice WorkspaceStrategySelection, parents recovery.StackedParents) (*adapter.PreparedView, *claim.ClaimedView, error) {
	services, err := m.layered()
	if err != nil {
		return nil, nil, err
	}
	grants := grantsForSources(sources)
	targetDirs, err := branchTargetDirs(binding.Root, grants)
	if err != nil {
		return nil, nil, err
	}
	projKey := enginepaths.ProjectKey(primarySourcePath(sources))
	projectID := workspaceProjectFromContext(ctx, projKey)
	metaDir := enginepaths.MetaDirForBranchRoot(binding.Root)
	_, hints, _ := workspaceSelectionFromContext(ctx)

	base := PreparationProgress{
		Strategy: preparationStrategy(choice.Selection), Stage: "snapshotting_branch",
		TotalBytes: 2 * choice.Selection.DeclaredCost.UncloneableBytes,
	}
	reportPreparation(ctx, base)
	cv, snap, err := services.coordinator.ClaimSelectedView(ctx, claim.ClaimRequest{
		Identity: claim.ClaimIdentity{WorkerID: binding.ID, ProjectID: projectID, ViewID: binding.ID, SourceGeneration: 1},
		Storage: claim.ClaimStorage{
			RuntimeDir:     filepath.Join(m.branchRoot, adapterRuntimeDirName, projKey),
			BackingDir:     filepath.Join(metaDir, "backing"),
			MetadataPath:   filepath.Join(metaDir, "view-metadata.json"),
			TargetRootDirs: targetDirs,
		},
		Grants: grants, FocusPathHints: hints, SelectionFacts: choice.Selection,
		ParentLayers:        parents.Layers,
		ParentCheckpointIDs: parents.CheckpointIDs,
		Materialization: claim.ClaimMaterialization{
			PristineDir:      enginepaths.PristineTreeForBranchRoot(binding.Root),
			StatManifestPath: enginepaths.StatManifestForBranchRoot(binding.Root),
			CopyBudget:       choice.CopyBudget,
			GitObjectStore:   services.privateGit.Store(),
			CloneFile:        m.cloneFile,
			Progress: func(p adapter.MaterializeProgress) {
				progress := base
				progress.Files, progress.Bytes = p.Entries, p.LogicalBytes
				reportPreparation(ctx, progress)
			},
		},
		Hooks: claim.ClaimHooks{Retention: services.retention, PrivateGit: services.privateGit},
	}, choice.Adapter, choice.Probe)
	if err != nil {
		var overrun *adapter.BudgetOverrunError
		if !errors.As(err, &overrun) {
			recordBranchClaimState(binding.Root, nil, snap, nil)
		}
		return nil, nil, err
	}
	if len(parents.Manifests) > 0 {
		if err := services.recovery.OverlayParentChainOnMaterializedView(cv.Prepared, parents.Manifests); err != nil {
			return nil, nil, errors.Join(err, rollBackClaim(ctx, services, binding.ID, binding.ID))
		}
	}
	if err := adoptMaterializedRepos(ctx, services, binding.ID, binding.Root, sources, targetDirs, cv.Prepared); err != nil {
		return nil, nil, errors.Join(err, rollBackClaim(ctx, services, binding.ID, binding.ID))
	}
	layered, err := bindMaterializedPublisher(cv, projectID, services)
	if err != nil {
		return nil, nil, errors.Join(err, rollBackClaim(ctx, services, binding.ID, binding.ID))
	}
	recordBranchClaimState(binding.Root, cv, snap, layered)
	return cv.Prepared, cv, nil
}

// bindMaterializedPublisher gives a materialized view what completion and explicit capture need. It
// registers no consumer Engine: the working tree is the view, so every reader already sees it.
func bindMaterializedPublisher(cv *claim.ClaimedView, projectID string, services *layeredServices) (*branchLayeredView, error) {
	prepared := cv.Prepared
	err := services.recovery.AdoptMaterializedView(recovery.RecordedView{
		WorkerID: cv.Identity.WorkerID, ViewID: prepared.ViewID, TaskID: cv.Identity.WorkerID, ProjectID: projectID, AdapterID: cv.Adapter.ID(),
	})
	if err != nil {
		return nil, err
	}
	publisher, err := newViewPublisher(cv, services.recovery.CheckpointStore(), services, viewRootSpecs(prepared))
	if err != nil {
		return nil, err
	}
	return &branchLayeredView{publisher: publisher, services: services, workerID: cv.Identity.WorkerID, viewID: prepared.ViewID}, nil
}

func viewRootSpecs(prepared *adapter.PreparedView) []layeredfs.RootSpec {
	roots := make([]layeredfs.RootSpec, 0, len(prepared.Roots))
	for _, r := range prepared.Roots {
		roots = append(roots, layeredfs.RootSpec{ID: r.RootID, Label: r.Label, TrunkPath: r.SourcePath, Subroot: r.Subroot, IsPrimary: r.IsPrimary})
	}
	return roots
}

func newViewPublisher(cv *claim.ClaimedView, store *checkpoint.Store, services *layeredServices, roots []layeredfs.RootSpec) (*checkpoint.Publisher, error) {
	prepared := cv.Prepared
	return checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: prepared.Core, Store: store, WorkerID: cv.Identity.WorkerID, TaskID: cv.Identity.WorkerID,
		ProjectID: cv.Identity.ProjectID, SourceGeneration: fmt.Sprint(cv.Identity.SourceGeneration), SelectedRoots: roots,
		ParentCheckpoints: cv.Metadata.Checkpoints.ParentCheckpointIDs,
		Callbacks: checkpoint.BarrierCallbacks{
			AcquireAdapterBarrier: func(ctx context.Context) (layeredfs.BarrierToken, func() error, error) {
				receipt, release, err := cv.Adapter.CheckpointBarrier(ctx, prepared.ViewID)
				if err != nil {
					return layeredfs.BarrierToken{}, nil, err
				}
				return receipt.Token, release, nil
			},
			CommitRetention: func(ctx context.Context, checkpointID string, blobs, gitObjects []string) error {
				return services.recovery.CommitCheckpointRetention(ctx, cv.Identity.WorkerID, checkpointID, blobs, gitObjects, cv.Metadata.Checkpoints.ParentCheckpointIDs)
			},
		},
	})
}

// settleMaterializedBranchView releases a materialized view. Nothing else holds its state, so there
// are no recorded writers or mount to settle first.
func settleMaterializedBranchView(ctx context.Context, view *branchLayeredView) error {
	if err := view.services.coordinator.ReleaseWorkerView(ctx, view.viewID, view.services.retention); err != nil {
		return err
	}
	return view.services.recovery.ForgetReleasedView(view.workerID, view.viewID)
}

// settleMaterializedRuntime settles a materialized branch before its data may go. It writes nothing
// durable, because a cancel must finish on a full volume and no mount or writer record exists.
func (m *Manager) settleMaterializedRuntime(ctx context.Context, branchRoot string) error {
	clean := filepath.Clean(branchRoot)
	branchClaimsMu.RLock()
	rec := branchClaimStates[clean]
	branchClaimsMu.RUnlock()
	if rec.layered != nil && rec.layered.engine == nil {
		if err := settleMaterializedBranchView(ctx, rec.layered); err != nil {
			return err
		}
	}
	m.forgetBranchViews(clean)
	releaseBranchClaimState(clean)
	return nil
}

func isMaterializedBranch(branchRoot string) bool {
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot))
	return err == nil && meta.PreparationKind == adapter.PreparationMaterialized
}

// settleEvictedMaterializedView drops the live view of a tree retention just reclaimed.
func (m *Manager) settleEvictedMaterializedView(ctx context.Context, branchRoot string) error {
	clean := filepath.Clean(branchRoot)
	branchClaimsMu.RLock()
	rec := branchClaimStates[clean]
	branchClaimsMu.RUnlock()
	if rec.layered == nil || rec.layered.engine != nil {
		releaseBranchClaimState(clean)
		return nil
	}
	if err := settleMaterializedBranchView(ctx, rec.layered); err != nil {
		return err
	}
	m.forgetBranchViews(clean)
	releaseBranchClaimState(clean)
	return os.RemoveAll(filepath.Join(enginepaths.MetaDirForBranchRoot(clean), "backing"))
}

const materializedGitFormat = 1

type materializedGitRecord struct {
	Format int                   `json:"format"`
	Views  []materializedGitView `json:"views"`
}

type materializedGitView struct {
	ViewID      string                          `json:"view_id"`
	WorkerID    string                          `json:"worker_id"`
	CoreViewID  string                          `json:"core_view_id,omitempty"`
	RootID      string                          `json:"root_id"`
	TrunkDir    string                          `json:"trunk_dir"`
	WorktreeDir string                          `json:"worktree_dir"`
	WorktreeRel string                          `json:"worktree_rel,omitempty"`
	BranchDir   string                          `json:"branch_dir,omitempty"`
	Repos       []privategit.MaterializedRepoAt `json:"repos,omitempty"`
}

func materializedGitRecordPath(branchRoot string) string {
	return filepath.Join(enginepaths.MetaDirForBranchRoot(branchRoot), "materialized-git.json")
}

// adoptMaterializedRepos hands the repositories preparation built to the private Git manager, one Git
// view per root. Only the primary root's carries the worker's identity, which is what Git admission
// looks a view up by. What each repository started from is recorded, because a later process that
// re-derives this worker's changes needs it to tell which refs the worker moved.
func adoptMaterializedRepos(
	ctx context.Context,
	services *layeredServices,
	workerID, branchRoot string,
	sources []projectroot.RootRef,
	targetDirs map[string]string,
	view *adapter.PreparedView,
) error {
	record := materializedGitRecord{Format: materializedGitFormat}
	for _, grant := range grantsForSources(sources) {
		worktreeDir := targetDirs[grant.RootID]
		worktreeRel, err := filepath.Rel(branchRoot, worktreeDir)
		if err != nil {
			return err
		}
		gitView := materializedGitView{
			ViewID: workerID, WorkerID: workerID, RootID: grant.RootID,
			TrunkDir: grant.SourcePath, WorktreeDir: worktreeDir, WorktreeRel: worktreeRel,
		}
		if len(sources) > 1 {
			if grant.IsPrimary {
				gitView.BranchDir = branchRoot
			} else {
				gitView.ViewID, gitView.WorkerID, gitView.CoreViewID = workerID+"-"+grant.RootID, workerID+"-"+grant.RootID, workerID
			}
		}
		repos := make([]privategit.SeededRepo, 0, len(view.SeededGitRepos))
		for key, seeded := range view.SeededGitRepos {
			rootID, repoRel, _ := strings.Cut(key, ":")
			if rootID != grant.RootID {
				continue
			}
			repos = append(repos, privategit.SeededRepo{
				RepoRel: repoRel, PrivateGitDir: seeded.PrivateMetaDir,
				Metrics: privategit.InitMetrics{ObjectsReused: seeded.ObjectsReused, BytesImported: seeded.BytesImported, BytesReused: seeded.BytesReused},
			})
		}
		if err := adoptGitView(ctx, services, gitView, view.Core, repos, nil); err != nil {
			return err
		}
		gitView.Repos = services.privateGit.MaterializedReposOf(gitView.ViewID)
		record.Views = append(record.Views, gitView)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(materializedGitRecordPath(branchRoot)),
		Source:   strings.NewReader(string(raw)), Mode: 0o600, DirMode: 0o700,
	})
	return err
}

func adoptGitView(ctx context.Context, services *layeredServices, gitView materializedGitView, core *layeredfs.Core, repos []privategit.SeededRepo, installed []privategit.MaterializedRepoAt) error {
	err := services.privateGit.AdoptMaterializedView(ctx, privategit.MaterializedViewBinding{
		ViewID: gitView.ViewID, WorkerID: gitView.WorkerID, RootID: gitView.RootID, CoreViewID: gitView.CoreViewID,
		TrunkDir: gitView.TrunkDir, WorktreeDir: gitView.WorktreeDir, Core: core, Repos: repos, Installed: installed,
	})
	if err != nil || gitView.BranchDir == "" {
		return err
	}
	return services.privateGit.BindViewBranch(gitView.ViewID, gitView.BranchDir)
}

// offerMaterializedBranchesForRecovery makes each materialized branch an earlier process left ready to
// be reopened. Nothing is read until a worker's completion or a capture asks, because that re-derives
// the worker's changes with the same stat-and-hash capture any other capture uses.
func (m *Manager) offerMaterializedBranchesForRecovery(ctx context.Context) error {
	trees, err := ListJobTrees(ctx, m.branchRoot)
	if err != nil {
		return err
	}
	for _, tree := range trees {
		if !tree.Present || !adapter.HasStatManifest(enginepaths.StatManifestForBranchRoot(tree.Root)) {
			continue
		}
		clean := filepath.Clean(tree.Root)
		branchClaimsMu.Lock()
		if _, known := branchClaimStates[clean]; !known {
			meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(clean))
			if err != nil {
				branchClaimsMu.Unlock()
				slog.WarnContext(ctx, "materialized branch metadata unreadable", "job_id", tree.JobID, "err", err)
				continue
			}
			root, jobID := clean, tree.JobID
			branchClaimStates[clean] = branchClaimRecord{
				state: snapshotFromJobMeta(clean, meta),
				reopen: func(c context.Context) (*branchLayeredView, error) {
					return m.reopenMaterializedBranch(c, root, jobID)
				},
			}
		}
		branchClaimsMu.Unlock()
		if err := m.republishLostCheckpoint(ctx, clean); err != nil {
			slog.WarnContext(ctx, "republish materialized checkpoint", "job_id", tree.JobID, "err", err)
			return err
		}
	}
	return nil
}

// republishLostCheckpoint serves a branch whose metadata names a checkpoint no store holds: if the
// checkpoint exists in a restored or relocated store, it keeps that checkpoint; otherwise it
// republishes from the materialized tree without wiping the prior metadata before publication succeeds.
func (m *Manager) republishLostCheckpoint(ctx context.Context, branchRoot string) error {
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	meta, err := LoadJobMeta(metaDir)
	if err != nil || meta.LastPublishedCheckpointID == "" {
		return err
	}
	if storeDir, cid, ok := BranchCheckpoint(branchRoot); ok && checkpointManifestExists(storeDir, cid) {
		store, err := checkpoint.NewStore(storeDir)
		if err != nil {
			return fmt.Errorf("%w: %w", recovery.ErrIncompleteCheckpoint, err)
		}
		if _, err := store.LoadManifest(cid); err != nil {
			return fmt.Errorf("%w: %w", recovery.ErrIncompleteCheckpoint, err)
		}
		if storeDir != meta.CheckpointStoreDir {
			meta.CheckpointStoreDir = storeDir
			if err := WriteJobMeta(metaDir, meta); err != nil {
				return err
			}
		}
		return nil
	}
	if m.recovery != nil && m.recovery.CheckpointStore() != nil {
		if _, err := m.recovery.CheckpointStore().LoadManifest(meta.LastPublishedCheckpointID); err == nil {
			return nil
		} else if !errors.Is(err, checkpoint.ErrCheckpointNotFound) {
			return fmt.Errorf("%w: %w", recovery.ErrIncompleteCheckpoint, err)
		}
	}
	_, err = PublishBranchCheckpoint(ctx, branchRoot)
	return err
}

func (m *Manager) reopenMaterializedBranch(ctx context.Context, branchRoot, jobID string) (*branchLayeredView, error) {
	services, err := m.layered()
	if err != nil {
		return nil, err
	}
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	meta, err := LoadJobMeta(metaDir)
	if err != nil {
		return nil, err
	}
	viewID := strings.TrimSpace(meta.ViewID)
	if viewID == "" {
		viewID = jobID
	}
	eager := adapter.NewEagerCopyAdapter(meta.FallbackReason)
	// The tree holds the result; the old backing only held what the last capture derived from it.
	backing := filepath.Join(metaDir, "backing")
	if err := os.RemoveAll(backing); err != nil {
		return nil, err
	}
	layout, err := layoutFromJobMeta(meta)
	if err != nil {
		return nil, err
	}
	targets, err := branchTargetDirs(branchRoot, grantsForSources(layout.Roots))
	if err != nil {
		return nil, err
	}
	view, err := eager.Reopen(ctx, adapter.ReopenRequest{
		ViewID: viewID, RuntimeDir: filepath.Join(m.branchRoot, adapterRuntimeDirName, filepath.Base(filepath.Dir(branchRoot))),
		BackingDir: backing, StatManifestPath: enginepaths.StatManifestForBranchRoot(branchRoot),
		WorkspaceDirs: targets, PristineDir: enginepaths.PristineTreeForBranchRoot(branchRoot),
	})
	if err != nil {
		return nil, err
	}
	projectID := filepath.Base(filepath.Dir(branchRoot))
	if journal, err := services.recovery.LoadWorkerJournal(jobID); err == nil && journal.ProjectID != "" {
		projectID = journal.ProjectID
	}
	cv, err := services.coordinator.AdoptPreparedView(ctx, claim.ClaimRequest{
		Identity: claim.ClaimIdentity{WorkerID: jobID, ProjectID: projectID, ViewID: viewID, SourceGeneration: 1},
		Hooks:    claim.ClaimHooks{Retention: services.retention, PrivateGit: services.privateGit},
	}, eager, view)
	if err != nil {
		return nil, err
	}
	if err := readoptMaterializedRepos(ctx, services, branchRoot, view.Core); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.Join(err, rollBackClaim(ctx, services, jobID, viewID))
	}
	layered, err := bindMaterializedPublisher(cv, projectID, services)
	if err != nil {
		return nil, errors.Join(err, rollBackClaim(ctx, services, jobID, viewID))
	}
	m.recordBranchViews(branchRoot, WorkspaceStrategySelection{
		AdapterID: eager.ID(), Strategy: ProvisionMaterializedEagerCopy, Facts: eager.Facts(), Adapter: eager,
		Selection: adapter.SelectionFacts{Provider: eager.ID(), Reason: meta.SelectionReason, DeclaredCost: meta.DeclaredCost},
	}, view)
	recordBranchClaimState(branchRoot, cv, cv.State, layered)
	return layered, nil
}

// adoptRecoveredMaterializedView gives a view recovery rebuilt on the materialized adapter what a
// claim gives a new one.
func (m *Manager) adoptRecoveredMaterializedView(ctx context.Context, branchRoot, jobID, projectID string, adp adapter.Adapter, view *adapter.PreparedView) error {
	services, err := m.layered()
	if err != nil {
		return err
	}
	cv, err := services.coordinator.AdoptPreparedView(ctx, claim.ClaimRequest{
		Identity: claim.ClaimIdentity{WorkerID: jobID, ProjectID: projectID, ViewID: view.ViewID, SourceGeneration: 1},
		Hooks:    claim.ClaimHooks{Retention: services.retention, PrivateGit: services.privateGit},
	}, adp, view)
	if err != nil {
		return err
	}
	layered, err := bindMaterializedPublisher(cv, projectID, services)
	if err != nil {
		return errors.Join(err, rollBackClaim(ctx, services, jobID, view.ViewID))
	}
	recordBranchClaimState(branchRoot, cv, cv.State, layered)
	return nil
}

// materializedViewOf finds a branch's materialized view: the live one, one an earlier process left
// that recovery offered, or, for a read alone, one reopened just to list changes.
func materializedViewOf(ctx context.Context, branchRoot string) (*adapter.PreparedView, bool, error) {
	clean := filepath.Clean(branchRoot)
	branchClaimsMu.RLock()
	rec, ok := branchClaimStates[clean]
	branchClaimsMu.RUnlock()
	if ok && rec.claimed != nil && rec.claimed.Prepared != nil {
		return rec.claimed.Prepared, rec.claimed.Prepared.Facts.PreparationKind == adapter.PreparationMaterialized, nil
	}
	if ok && rec.reopen != nil {
		if _, err := rec.reopen(ctx); err != nil {
			return nil, true, err
		}
		return materializedViewOf(ctx, clean)
	}
	manifest := enginepaths.StatManifestForBranchRoot(clean)
	if !adapter.HasStatManifest(manifest) {
		return nil, false, nil
	}
	view, err := reopenForRead(ctx, clean, manifest)
	return view, true, err
}

func reopenForRead(ctx context.Context, branchRoot, manifest string) (*adapter.PreparedView, error) {
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	viewID := filepath.Base(branchRoot) + "-read"
	eager := adapter.NewEagerCopyAdapter(adapter.ReasonNone)
	if view, ok := eager.LoadedView(viewID); ok {
		return view, nil
	}
	return eager.Reopen(ctx, adapter.ReopenRequest{
		ViewID: viewID, RuntimeDir: filepath.Join(metaDir, "read"), BackingDir: filepath.Join(metaDir, "read", "backing"), StatManifestPath: manifest,
	})
}

func materializedDivergencePaths(ctx context.Context, branchRoot string) ([]string, bool, error) {
	view, ok, err := materializedViewOf(ctx, branchRoot)
	if !ok || err != nil {
		return nil, ok, err
	}
	changed, _, err := adapter.NewEagerCopyAdapter(adapter.ReasonNone).ChangedPaths(ctx, view.ViewID)
	if err != nil {
		return nil, true, err
	}
	out := make([]string, 0, len(changed))
	for _, qp := range changed {
		if consumer.IsInternalStoragePath(qp.Rel) {
			continue
		}
		p := qp.Rel
		if len(view.Roots) > 1 {
			p = qp.RootID + "/" + qp.Rel
		}
		out = append(out, p)
	}
	return out, true, nil
}

func materializedChangedWorkingFiles(ctx context.Context, branchRoot string) ([]recovery.ChangedWorkingFile, error) {
	view, ok, err := materializedViewOf(ctx, branchRoot)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("branch %s has no materialized view", branchRoot)
	}
	reader := adapter.NewEagerCopyAdapter(adapter.ReasonNone)
	changed, _, err := reader.ChangedPaths(ctx, view.ViewID)
	if err != nil {
		return nil, err
	}
	workspaces := make(map[string]string, len(view.Roots))
	for _, r := range view.Roots {
		workspaces[r.RootID] = r.WorkspacePath
	}
	out := make([]recovery.ChangedWorkingFile, 0, len(changed))
	for _, qp := range changed {
		abs := filepath.Join(workspaces[qp.RootID], filepath.FromSlash(qp.Rel))
		present, err := entryInBranchTree(workspaces[qp.RootID], abs)
		if err != nil {
			return nil, err
		}
		// A directory has no bytes to keep; its changed children are entries themselves.
		if info, err := os.Lstat(abs); present && err == nil && info.IsDir() {
			continue
		}
		if !present && reader.IsDirectoryBase(view.ViewID, qp) {
			continue
		}
		file := recovery.ChangedWorkingFile{Path: qp}
		if present {
			file.Source = abs
		}
		out = append(out, file)
	}
	return out, nil
}
