package recovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// changedFilesDirName holds a materialized worker's changed paths inside its quarantine, by
// root id and root-relative path. A path recorded as affected with nothing here was deleted.
const changedFilesDirName = "changed"

// ChangedWorkingFile is one changed path of a materialized tree. An empty Source means the
// path no longer exists in the tree.
type ChangedWorkingFile struct {
	Path   layeredfs.QualifiedPath
	Source string
}

// PreserveChangedWorkingFiles retains the changed paths of a canceled materialized worker in
// its view's quarantine, under the same record and inventory as interrupted mounted work.
// Nothing else holds a materialized worker's unpromoted bytes once its tree is deleted.
func (c *Coordinator) PreserveChangedWorkingFiles(workerID, viewID, diag string, changed []ChangedWorkingFile) (*InterruptedWorkingRecord, error) {
	if len(changed) == 0 {
		return nil, nil
	}
	return preserveInQuarantine(c, InterruptedWorkingRecord{
		WorkerID: workerID, ViewID: viewID, InterruptedPhase: PhaseRelease, Diagnostic: diag,
	}, func(staging string) (salvagedWork, error) {
		work := salvagedWork{op: "cancel", paths: make([]layeredfs.QualifiedPath, 0, len(changed))}
		dir := filepath.Join(staging, changedFilesDirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return work, err
		}
		for _, file := range changed {
			rel := filepath.Join(file.Path.RootID, filepath.FromSlash(file.Path.Rel))
			if file.Path.RootID == "" || file.Path.Rel == "" || !filepath.IsLocal(rel) {
				return work, fmt.Errorf("%w: changed path %q in root %q", ErrCorruptRecoveryMetadata, file.Path.Rel, file.Path.RootID)
			}
			work.paths = append(work.paths, file.Path)
			if file.Source == "" {
				continue
			}
			written, err := retainChangedEntry(file.Source, filepath.Join(dir, rel))
			work.bytes += written
			if err != nil {
				return work, err
			}
		}
		return work, nil
	})
}

// retainChangedEntry keeps what a worker can have left at the path. A directory or special
// file has no bytes of its own: the path is recorded and its children are entries themselves.
func retainChangedEntry(src, dst string) (int64, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return 0, err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return 0, err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return 0, err
		}
		return 0, os.Symlink(link, dst)
	case info.Mode().IsRegular():
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return 0, err
		}
		written, err := copyWorkingFile(src, dst)
		if errors.Is(err, fs.ErrPermission) {
			// The worker removed its own read bit. The tree is about to go, so restoring
			// it costs nothing and the bytes survive.
			if err := os.Chmod(src, info.Mode().Perm()|0o400); err != nil {
				return 0, err
			}
			return copyWorkingFile(src, dst)
		}
		return written, err
	default:
		return 0, nil
	}
}
