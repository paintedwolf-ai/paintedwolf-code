package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// RetainCanceledWork keeps the bytes of a canceled materialized worker's changed paths before
// its tree may be deleted. A mounted view is skipped: settlement already salvaged its delta.
// Failure leaves the tree in place and reports the worker unresolved.
func (m *Manager) RetainCanceledWork(ctx context.Context, jobID, root, baselinePath string) error {
	if m.recovery == nil {
		return nil
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	viewID := jobID
	roots := []projectroot.RootRef{{ID: "primary", IsPrimary: true}}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(root))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return unresolvedRetention(jobID, viewID, root, err)
	case meta.PreparationKind == adapter.PreparationMounted:
		return nil
	default:
		if strings.TrimSpace(meta.ViewID) != "" {
			viewID = meta.ViewID
		}
		layout, err := layoutFromJobMeta(meta)
		if err != nil {
			return unresolvedRetention(jobID, viewID, root, err)
		}
		roots = layout.Roots
	}
	changed, err := changedWorkingFiles(ctx, root, baselinePath, roots)
	if err != nil {
		return unresolvedRetention(jobID, viewID, root, err)
	}
	if _, err := m.recovery.PreserveChangedWorkingFiles(jobID, viewID, "changed paths of a canceled worker", changed); err != nil {
		return unresolvedRetention(jobID, viewID, root, err)
	}
	return nil
}

func changedWorkingFiles(ctx context.Context, root, baselinePath string, roots []projectroot.RootRef) ([]recovery.ChangedWorkingFile, error) {
	baseline, err := workspacebaseline.Open(ctx, baselinePath, workspacebaseline.ContentStore(baselinePath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = baseline.Close() }()
	var changed []recovery.ChangedWorkingFile
	err = baseline.EachChange(ctx, roots, root, func(path string) error {
		location, err := workspacebaseline.LocateInBranch(roots, root, path)
		if err != nil {
			return err
		}
		present, err := entryInBranchTree(root, location.Abs)
		if err != nil {
			return err
		}
		file := recovery.ChangedWorkingFile{Path: layeredfs.QualifiedPath{RootID: location.RootID, Rel: location.Rel}}
		if present {
			file.Source = location.Abs
		}
		changed = append(changed, file)
		return nil
	})
	return changed, err
}

// entryInBranchTree reports whether abs names an entry held by the tree itself. A symlink above
// it means the entry lives wherever the link points, which is never the worker's to retain;
// the link is its own changed path.
func entryInBranchTree(root, abs string) (bool, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false, err
	}
	dir := root
	for _, part := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if entryAbsent(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			return false, nil
		}
	}
	_, err = os.Lstat(abs)
	if entryAbsent(err) {
		return false, nil
	}
	return err == nil, err
}

func entryAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func unresolvedRetention(jobID, viewID, root string, err error) error {
	return &recovery.RuntimeBlockedError{Report: recovery.WorkerRuntimeReport{
		WorkerID: jobID, ViewID: viewID, Disposition: recovery.RuntimeUnresolved, Reason: recovery.ReasonSalvageFailed,
		Err: &recovery.UnresolvedSettlementError{
			WorkerID: jobID, ViewID: viewID, RetainedCustody: true, RetainedStorageRefs: []string{root},
			Reason: fmt.Sprintf("retain changed paths of canceled worker: %v", err), Cause: err,
		},
	}}
}
