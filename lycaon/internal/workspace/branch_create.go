package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
)

type branchClaimRecord struct {
	claimed *claim.ClaimedView
	state   claim.WorkspaceStateSnapshot
	layered *branchLayeredView
}

var (
	branchClaimsMu    sync.RWMutex
	branchClaimStates = make(map[string]branchClaimRecord)
)

func recordBranchClaimState(branchRoot string, cv *claim.ClaimedView, snap claim.WorkspaceStateSnapshot, layered *branchLayeredView) {
	branchClaimsMu.Lock()
	branchClaimStates[filepath.Clean(branchRoot)] = branchClaimRecord{claimed: cv, state: snap, layered: layered}
	branchClaimsMu.Unlock()
}

func releaseBranchClaimState(branchRoot string) {
	branchClaimsMu.Lock()
	delete(branchClaimStates, filepath.Clean(branchRoot))
	branchClaimsMu.Unlock()
}

// LookupBranchClaimedView returns the active ClaimedView recorded for a mounted worker branch (#1662).
func LookupBranchClaimedView(branchRoot string) (*claim.ClaimedView, bool) {
	branchClaimsMu.RLock()
	defer branchClaimsMu.RUnlock()
	rec, ok := branchClaimStates[filepath.Clean(branchRoot)]
	if !ok || rec.claimed == nil {
		return nil, false
	}
	return rec.claimed, true
}

// BranchWorkspaceState returns the typed WorkspaceStateSnapshot and DispatchWorkspaceReceipt for a branch (#1662).
func BranchWorkspaceState(branchRoot string) (claim.WorkspaceStateSnapshot, claim.DispatchWorkspaceReceipt, bool) {
	clean := filepath.Clean(branchRoot)
	branchClaimsMu.RLock()
	rec, ok := branchClaimStates[clean]
	branchClaimsMu.RUnlock()
	if ok {
		return rec.state, claim.BuildReceipt(rec.state), true
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(clean))
	if err != nil {
		return claim.WorkspaceStateSnapshot{}, claim.DispatchWorkspaceReceipt{}, false
	}
	snap := snapshotFromJobMeta(clean, meta)
	return snap, claim.BuildReceipt(snap), true
}

const provisionLockSuffix = ".provision.lock"

func tryWorkspaceProvisionLock(branchRoot string) (func(), error) {
	file, err := filelock.Open(branchRoot + provisionLockSuffix)
	if err != nil {
		return nil, err
	}
	held, err := filelock.TryExclusive(file)
	if err != nil || !held {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrBranchInUse
	}
	return func() {
		_ = filelock.Unlock(file)
		_ = file.Close()
	}, nil
}

func acquireWorkspaceProvisionLock(ctx context.Context, branchRoot string) (func(), error) {
	file, err := filelock.Open(branchRoot + provisionLockSuffix)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, lockErr := filelock.TryExclusive(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, lockErr
		}
		if held {
			return func() {
				_ = filelock.Unlock(file)
				_ = file.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func prepareMountedWorkspaceView(ctx context.Context, m *Manager, binding *Binding, sources []projectroot.RootRef, choice WorkspaceStrategySelection, parents recovery.StackedParents) (*adapter.PreparedView, *claim.ClaimedView, error) {
	services, err := m.layered()
	if err != nil {
		return nil, nil, err
	}
	grants := make([]adapter.RootGrant, 0, len(sources))
	targetDirs := make(map[string]string, len(sources))
	primaryPath := sources[0].Path
	for _, s := range sources {
		id := s.ID
		if id == "" {
			id = "primary"
		}
		label := s.Label
		if label == "" {
			label = id
		}
		grants = append(grants, adapter.RootGrant{RootID: id, Label: label, SourcePath: s.Path, IsPrimary: s.IsPrimary})
		if len(sources) == 1 {
			targetDirs[id] = binding.Root
		} else {
			dir, err := projectroot.BranchDirForID(id)
			if err != nil {
				return nil, nil, err
			}
			targetDirs[id] = filepath.Join(binding.Root, dir)
		}
		if s.IsPrimary {
			primaryPath = s.Path
		}
	}
	tracker, err := observation.NewTracker(observation.TrackerConfig{ViewID: binding.ID, Facts: observation.UnknownVisibilityFacts(string(choice.AdapterID))})
	if err != nil {
		return nil, nil, err
	}
	maxEntries, maxBytes := effectiveLimits(ctx, m)
	_, hints, _ := workspaceSelectionFromContext(ctx)
	projKey := enginepaths.ProjectKey(primaryPath)
	projectID := workspaceProjectFromContext(ctx, projKey)
	metaDir := enginepaths.MetaDirForBranchRoot(binding.Root)
	cv, snap, err := services.coordinator.ClaimSelectedView(ctx, claim.ClaimRequest{
		Identity: claim.ClaimIdentity{
			WorkerID:         binding.ID,
			ProjectID:        projectID,
			ViewID:           binding.ID,
			SourceGeneration: 1,
		},
		Storage: claim.ClaimStorage{
			RuntimeDir:     filepath.Join(m.branchRoot, adapterRuntimeDirName, projKey),
			BackingDir:     filepath.Join(metaDir, "backing"),
			MetadataPath:   filepath.Join(metaDir, "view-metadata.json"),
			TargetRootDirs: targetDirs,
		},
		Grants:              grants,
		FocusPathHints:      hints,
		ParentLayers:        parents.Layers,
		ParentCheckpointIDs: parents.CheckpointIDs,
		Limits:              claim.ClaimLimits{MaxEntries: maxEntries, MaxBytes: maxBytes},
		Hooks: claim.ClaimHooks{
			Retention:         services.retention,
			VerifyConfinement: m.selector.VerifyViewConfinement,
			PrivateGit:        services.privateGit,
			Quiesce:           viewWritersHeldByBarrier,
			FlushClient:       viewWritersHeldByBarrier,
			Observations:      tracker,
		},
	}, choice.Adapter, choice.Probe)
	if err != nil {
		recordBranchClaimState(binding.Root, nil, snap, nil)
		return nil, nil, err
	}
	if len(sources) > 1 {
		if err := services.privateGit.BindViewBranch(binding.ID, binding.Root); err != nil {
			return nil, nil, errors.Join(err, rollBackClaim(ctx, services, binding.ID, binding.ID))
		}
	}
	layered, err := bindViewConsumer(binding.Root, cv, projectID, tracker, services)
	if err != nil {
		return nil, nil, errors.Join(err, rollBackClaim(ctx, services, binding.ID, binding.ID))
	}
	recordBranchClaimState(binding.Root, cv, snap, layered)
	return cv.Prepared, cv, nil
}

// rollBackClaim releases a view its claim could not finish. A failure stays in the coordinator's
// custody and the recovery records stay, so a retry or startup settles the same facts.
func rollBackClaim(ctx context.Context, services *layeredServices, workerID, viewID string) error {
	if err := services.coordinator.ReleaseWorkerView(ctx, viewID, services.retention); err != nil {
		return err
	}
	return services.recovery.ForgetReleasedView(workerID, viewID)
}

// adoptRecoveredMountedView gives a view recovery rebuilt the records a claim gives a new one: an
// Engine for its consumers, a publisher for completion, and settlement through the coordinator.
func (m *Manager) adoptRecoveredMountedView(ctx context.Context, branchRoot, jobID, projectID string, adp adapter.Adapter, view *adapter.PreparedView) error {
	services, err := m.layered()
	if err != nil {
		return err
	}
	tracker, err := observation.NewTracker(observation.TrackerConfig{ViewID: view.ViewID, Facts: observation.UnknownVisibilityFacts(string(adp.ID()))})
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
	layered, err := bindViewConsumer(branchRoot, cv, projectID, tracker, services)
	if err != nil {
		return errors.Join(err, rollBackClaim(ctx, services, jobID, view.ViewID))
	}
	recordBranchClaimState(branchRoot, cv, cv.State, layered)
	return nil
}

// viewWritersHeldByBarrier is the barrier's writer step for an engine-served view. The server holds
// every mutation at the gate for the barrier's whole span, including from a worker's background
// process, so there is nothing further to stop and no writable mapping the engine holds.
func viewWritersHeldByBarrier(context.Context, string) error { return nil }

func materializeRootWithAdapter(
	ctx context.Context, m *Manager, source projectroot.RootRef,
	sourceRoot, seedTree, branchRoot, runtimeDir, viewID string,
	base PreparationProgress, maxEntries, maxBytes int64, fallbackReason adapter.ReasonCode,
) (*adapter.PreparedView, mirrorResult, error) {
	rootID := strings.TrimSpace(source.ID)
	if rootID == "" {
		rootID = "primary"
	}
	label := strings.TrimSpace(source.Label)
	if label == "" {
		label = rootID
	}
	if strings.TrimSpace(viewID) == "" {
		viewID = uuid.NewString()
	}
	if strings.TrimSpace(runtimeDir) == "" {
		runtimeDir = filepath.Join(enginepaths.MetaDirForBranchRoot(branchRoot), "adapter")
	}
	services, err := m.layered()
	if err != nil {
		return nil, mirrorResult{}, err
	}
	gitStore := services.privateGit.Store()
	var res mirrorResult
	req := adapter.PrepareRequest{
		ViewID: viewID, RuntimeDir: runtimeDir,
		TargetRootDirs: map[string]string{rootID: branchRoot},
		Grants:         []adapter.RootGrant{{RootID: rootID, Label: label, SourcePath: sourceRoot, IsPrimary: true}},
		MaxEntries:     maxEntries, MaxBytes: maxBytes, GitObjectStore: gitStore,
		ProgressFn: func(_ adapter.FallbackProgress) {
			progress := base
			progress.Files, progress.Bytes = res.Files, res.Bytes
			reportPreparation(ctx, progress)
		},
		FileSeeder: func(cCtx context.Context, _ string, rel, srcPath, dstPath string, info fs.FileInfo) (bool, int64, string, error) {
			effectiveSrc := srcPath
			if seedTree != "" {
				candidate := filepath.Join(seedTree, filepath.FromSlash(rel))
				if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
					effectiveSrc = candidate
				}
			}
			cloned, err := cloneOrCopyFile(cCtx, effectiveSrc, dstPath, info.Mode(), info.ModTime())
			if err != nil {
				return false, 0, "", err
			}
			sz := max(info.Size(), 0)
			if st, err := os.Stat(dstPath); err == nil {
				sz = max(st.Size(), 0)
			}
			res.Files++
			res.Bytes += sz
			if cloned {
				res.ClonedFiles++
				res.ClonedBytes += sz
			} else {
				res.CopiedBytes += sz
			}
			progress := base
			progress.Files, progress.Bytes = res.Files, res.Bytes
			reportPreparation(cCtx, progress)
			shaHex, err := sha256File(dstPath)
			return cloned, sz, shaHex, err
		},
	}
	view, err := PrepareMaterializedSeedView(ctx, m.seedRoot, req, fallbackReason)
	return view, res, err
}

// refuseLiveBranchView stops a rebuild from recursing through a mount or discarding an unsettled
// view. A record whose view something else settled, such as recovery, no longer holds the branch.
func (m *Manager) refuseLiveBranchView(branchRoot, jobID string) error {
	if err := RefuseMountedBranch(branchRoot); err != nil {
		return err
	}
	services, err := m.layered()
	if err != nil {
		return err
	}
	if services.coordinator.SettlementUnresolved(jobID) {
		return fmt.Errorf("%w: view %q", claim.ErrSettlementUnresolved, jobID)
	}
	branchClaimsMu.RLock()
	rec := branchClaimStates[filepath.Clean(branchRoot)]
	branchClaimsMu.RUnlock()
	if rec.layered == nil {
		return nil
	}
	if supervisor, ok := rec.claimed.Adapter.(adapter.MountSupervisor); ok && rec.layered.services == services && supervisor.IsViewActive(rec.layered.viewID) {
		return ErrBranchInUse
	}
	forgetSettledBranchClaim(branchRoot)
	return nil
}

// forgetSettledBranchClaim drops the live records of a view another path settled through its adapter.
func forgetSettledBranchClaim(branchRoot string) {
	branchClaimsMu.RLock()
	rec := branchClaimStates[filepath.Clean(branchRoot)]
	branchClaimsMu.RUnlock()
	if rec.layered != nil {
		consumer.UnregisterEngine(rec.layered.engine)
		rec.layered.services.coordinator.ForgetSettledView(rec.layered.viewID)
	}
	releaseBranchClaimState(branchRoot)
}

func (m *Manager) createBranchSkeleton(
	ctx context.Context,
	branchRoot string,
	layout SandboxLayout,
	jobID string,
) (*Binding, error) {
	if err := m.refuseLiveBranchView(branchRoot, jobID); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(branchRoot), 0o700); err != nil {
		return nil, classifyProvisionError(err)
	}
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	_ = os.RemoveAll(metaDir)
	if err := os.RemoveAll(branchRoot); err != nil {
		return nil, classifyProvisionError(err)
	}
	if err := os.MkdirAll(branchRoot, 0o750); err != nil {
		return nil, classifyProvisionError(err)
	}
	if len(layout.Roots) > 1 {
		for _, root := range layout.Roots {
			dir, err := projectroot.BranchDirForID(root.ID)
			if err != nil {
				_ = os.RemoveAll(branchRoot)
				return nil, err
			}
			if err := os.MkdirAll(filepath.Join(branchRoot, dir), 0o750); err != nil {
				_ = os.RemoveAll(branchRoot)
				return nil, classifyProvisionError(err)
			}
		}
	}
	if err := WriteJobMeta(metaDir, jobMetaFromLayout(layout)); err != nil {
		_ = os.RemoveAll(branchRoot)
		_ = os.RemoveAll(metaDir)
		return nil, err
	}
	reportPreparation(ctx, PreparationProgress{Stage: "branch_created"})
	return &Binding{ID: jobID, Root: branchRoot}, nil
}

func validateBranchDirectories(branchRoot string, layout SandboxLayout) error {
	if len(layout.Roots) == 1 {
		return nil
	}
	for _, root := range layout.Roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		if err != nil {
			return err
		}
		path := filepath.Join(branchRoot, dir)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("worker branch root %q is unavailable: %w", root.ID, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("worker branch root %q is not a directory", root.ID)
		}
	}
	return nil
}
