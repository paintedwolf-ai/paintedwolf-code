package recovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	// changedFilesDirName holds a materialized worker's changed paths inside its quarantine, by
	// root id and root-relative path. A path recorded as affected with nothing here was deleted.
	changedFilesDirName = "changed"
	// wholeTreeDirName holds a whole tree whose changes could not be listed.
	wholeTreeDirName = "tree"
)

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
		for _, file := range changed {
			rel := filepath.Join(file.Path.RootID, filepath.FromSlash(file.Path.Rel))
			if file.Path.RootID == "" || file.Path.Rel == "" || !filepath.IsLocal(rel) {
				return work, fmt.Errorf("%w: changed path %q in root %q", ErrCorruptRecoveryMetadata, file.Path.Rel, file.Path.RootID)
			}
			work.paths = append(work.paths, file.Path)
			if file.Source == "" {
				continue
			}
			written, err := retainEntry(c, file.Source, staging, filepath.Join(changedFilesDirName, rel))
			work.bytes += written
			if err != nil {
				return work, err
			}
		}
		return work, nil
	})
}

// PreserveWholeWorkingTree retains a canceled tree whose changes cannot be listed, so nothing
// can say which paths matter. The record says the scope is unknown. Linking costs no file data
// even though every entry is kept.
func (c *Coordinator) PreserveWholeWorkingTree(workerID, viewID, diag, tree string) (*InterruptedWorkingRecord, error) {
	return preserveInQuarantine(c, InterruptedWorkingRecord{
		WorkerID: workerID, ViewID: viewID, InterruptedPhase: PhaseRelease, Diagnostic: diag,
	}, func(staging string) (salvagedWork, error) {
		work := salvagedWork{op: "cancel", unknownScope: true}
		err := filepath.WalkDir(tree, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(tree, path)
			if err != nil {
				return err
			}
			target := filepath.Join(wholeTreeDirName, rel)
			if entry.IsDir() {
				// A directory the worker closed must still open; the tree is about to go.
				if info, err := entry.Info(); err != nil {
					return err
				} else if info.Mode().Perm()&0o700 != 0o700 {
					if err := os.Chmod(path, info.Mode().Perm()|0o700); err != nil {
						return err
					}
				}
				return os.MkdirAll(filepath.Join(staging, target), 0o700)
			}
			written, err := retainEntry(c, path, staging, target)
			work.bytes += written
			return err
		})
		return work, err
	})
}

// retainEntry keeps what a worker can have left at the path. A directory or special file has
// no bytes of its own: a changed directory's children are entries themselves.
func retainEntry(c *Coordinator, src, staging, rel string) (int64, error) {
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
		if err := os.MkdirAll(filepath.Dir(filepath.Join(staging, rel)), 0o700); err != nil {
			return 0, err
		}
		return 0, fseffect.Link(fseffect.LinkRequest{Location: fseffect.Location{Root: staging, Rel: rel}, Target: link})
	case info.Mode().IsRegular():
		return retainRegularFile(c, src, staging, rel, info)
	default:
		return 0, nil
	}
}

// retainRegularFile links, which keeps the mode, needs no read permission and costs no file
// data, so a worker that filled the disk can still be canceled. Copying is for volumes where
// a link cannot exist, and fails honestly when the bytes do not fit.
func retainRegularFile(c *Coordinator, src, staging, rel string, info fs.FileInfo) (int64, error) {
	err := injectFault(c, PhaseRelease, "link_changed_file")
	if err == nil {
		err = fseffect.HardLink(fseffect.PathLocation(src), fseffect.Location{Root: staging, Rel: rel})
	}
	if err == nil {
		return info.Size(), nil
	}
	if !linkImpossible(err) {
		return 0, err
	}
	if err := injectFault(c, PhaseRelease, "copy_changed_file"); err != nil {
		return 0, err
	}
	dst := filepath.Join(staging, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return 0, err
	}
	written, err := copyWorkingFile(src, dst)
	if err != nil {
		return written, err
	}
	return written, os.Chmod(dst, info.Mode().Perm())
}

func linkImpossible(err error) bool {
	for _, cause := range []error{syscall.EXDEV, syscall.EMLINK, syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.EPERM, fseffect.ErrUnsupported} {
		if errors.Is(err, cause) {
			return true
		}
	}
	return false
}
