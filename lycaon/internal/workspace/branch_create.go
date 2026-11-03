package workspace

import (
	"context"
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
	"github.com/lycaon/lycaon/internal/projectroot"
)

type branchClaimRecord struct {
	claimed *claim.ClaimedView
	state   claim.WorkspaceStateSnapshot
}

var (
	branchClaimsMu    sync.RWMutex
	branchClaimStates = make(map[string]branchClaimRecord)
)

func recordBranchClaimState(branchRoot string, cv *claim.ClaimedView, snap claim.WorkspaceStateSnapshot) {
	branchClaimsMu.Lock()
	branchClaimStates[filepath.Clean(branchRoot)] = branchClaimRecord{claimed: cv, state: snap}
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

func prepareMountedWorkspaceView(ctx context.Context, m *Manager, binding *Binding, sources []projectroot.RootRef, adp adapter.Adapter) (*adapter.PreparedView, *claim.ClaimedView, error) {
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
	maxEntries, maxBytes := effectiveLimits(ctx, m)
	_, hints, _ := workspaceSelectionFromContext(ctx)
	projKey := enginepaths.ProjectKey(primaryPath)
	coord := claim.NewCoordinator(adapter.NewSelector("", adp, nil, adp))
	cv, snap, err := coord.ClaimWorkerView(ctx, claim.ClaimRequest{
		Identity: claim.ClaimIdentity{
			WorkerID:         binding.ID,
			ProjectID:        projKey,
			ViewID:           binding.ID,
			SourceGeneration: 1,
		},
		Storage: claim.ClaimStorage{
			RuntimeDir:     filepath.Join(m.branchRoot, ".adapter", projKey),
			TargetRootDirs: targetDirs,
		},
		Grants:         grants,
		FocusPathHints: hints,
		Limits:         claim.ClaimLimits{MaxEntries: maxEntries, MaxBytes: maxBytes},
	})
	if err != nil {
		recordBranchClaimState(binding.Root, nil, snap)
		return nil, nil, err
	}
	recordBranchClaimState(binding.Root, cv, snap)
	return cv.Prepared, cv, nil
}

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
	var res mirrorResult
	req := adapter.PrepareRequest{
		ViewID: viewID, RuntimeDir: runtimeDir,
		TargetRootDirs: map[string]string{rootID: branchRoot},
		Grants:         []adapter.RootGrant{{RootID: rootID, Label: label, SourcePath: sourceRoot, IsPrimary: true}},
		MaxEntries:     maxEntries, MaxBytes: maxBytes,
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

func (m *Manager) createBranchSkeleton(
	ctx context.Context,
	branchRoot string,
	layout SandboxLayout,
	jobID string,
) (*Binding, error) {
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
