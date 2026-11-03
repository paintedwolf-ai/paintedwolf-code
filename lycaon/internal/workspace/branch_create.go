package workspace

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/projectroot"
)

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

func prepareMountedWorkspaceView(ctx context.Context, m *Manager, binding *Binding, sources []projectroot.RootRef, adp adapter.Adapter) (*adapter.PreparedView, error) {
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
				return nil, err
			}
			targetDirs[id] = filepath.Join(binding.Root, dir)
		}
		if s.IsPrimary {
			primaryPath = s.Path
		}
	}
	maxEntries, maxBytes := effectiveLimits(ctx, m)
	return adp.Prepare(ctx, adapter.PrepareRequest{
		ViewID:         binding.ID,
		RuntimeDir:     filepath.Join(m.branchRoot, ".adapter", enginepaths.ProjectKey(primaryPath)),
		TargetRootDirs: targetDirs,
		Grants:         grants,
		MaxEntries:     maxEntries,
		MaxBytes:       maxBytes,
	})
}

func materializeRootWithAdapter(
	ctx context.Context,
	m *Manager,
	source projectroot.RootRef,
	sourceRoot, seedTree, branchRoot, runtimeDir, viewID string,
	base PreparationProgress,
	maxEntries, maxBytes int64,
	fallbackReason adapter.ReasonCode,
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
		ViewID:         viewID,
		RuntimeDir:     runtimeDir,
		TargetRootDirs: map[string]string{rootID: branchRoot},
		Grants: []adapter.RootGrant{
			{RootID: rootID, Label: label, SourcePath: sourceRoot, IsPrimary: true},
		},
		MaxEntries: maxEntries,
		MaxBytes:   maxBytes,
		ProgressFn: func(fp adapter.FallbackProgress) {
			progress := base
			progress.Files = res.Files
			progress.Bytes = res.Bytes
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
			progress.Files = res.Files
			progress.Bytes = res.Bytes
			reportPreparation(cCtx, progress)
			shaHex, err := sha256File(dstPath)
			if err != nil {
				return cloned, sz, "", err
			}
			return cloned, sz, shaHex, nil
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
