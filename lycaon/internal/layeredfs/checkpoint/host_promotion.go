package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

// HostPromotion joins immutable checkpoint effects to the host's SQL commit.
// It owns undo independently of the component's roll-forward checkpoint journal.
type HostPromotion struct {
	checkpoint        *Promoter
	store             *Store
	targets           *promotionTargets
	authorizedGitDirs []string
	activeTx          *hostTransaction
	activeIndex       map[layeredfs.QualifiedPath]int
	retainLink        func(fseffect.HardLinkRequest) error
}

func NewHostPromotion(checkpoint *Promoter) *HostPromotion {
	return &HostPromotion{checkpoint: checkpoint, store: checkpoint.cfg.Store, targets: checkpoint.targets, authorizedGitDirs: checkpoint.cfg.AuthorizedGitDirs, retainLink: fseffect.HardLink}
}

// Apply persists undo and staged post-images before any checkpoint publication.
// SQL completion remains the caller's commit boundary.
func (host *HostPromotion) Apply(ctx context.Context, req ApplyRequest) (*PromotionJournalRecord, error) {
	host.checkpoint.applyMu.Lock()
	defer host.checkpoint.applyMu.Unlock()
	if err := host.prepare(ctx, req.Preview); err != nil {
		return nil, err
	}
	host.targets.recordPostimage = host.recordHostPostimage
	defer func() {
		host.targets.recordPostimage = nil
		host.activeTx = nil
		host.activeIndex = nil
	}()
	importRefs := func(ctx context.Context, _ string, workerID string, refs PrivateGitSnapshot) (map[string]string, error) {
		return ImportAndValidateCheckpointGitRefs(ctx, host.store.RootDir(), req.Preview.CheckpointID, workerID, refs, host.targets.roots, host.authorizedGitDirs...)
	}
	return host.checkpoint.apply(ctx, req, importRefs)
}

func (host *HostPromotion) removeTargetEntry(path layeredfs.QualifiedPath) error {
	root, err := host.targets.openTargetRoot(path.RootID)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return withWritableParent(root, path.Rel, func() error {
		if err := root.Remove(path.Rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func (host *HostPromotion) finalizeRestoredDirectories(shallowToDeep []hostTargetUndo) error {
	for i := len(shallowToDeep) - 1; i >= 0; i-- {
		target := shallowToDeep[i]
		if target.Before.Kind != layeredfs.EntryDirectory {
			continue
		}
		root, err := host.targets.openTargetRoot(target.Path.RootID)
		if err != nil {
			return err
		}
		err = restoreTargetFileMetadata(root, target.Path.Rel, target.Before.Mode, target.Before.Xattrs)
		_ = root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func restoreTargetFileMetadata(root *os.Root, rel string, mode os.FileMode, xattrs map[string][]byte) error {
	file, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if info, sErr := file.Stat(); sErr == nil && info.Mode().Perm()&0o200 == 0 {
		_ = file.Chmod(info.Mode().Perm() | 0o200)
	}
	if err := restoreFileXattrs(file, xattrs); err != nil {
		return err
	}
	if mode != 0 {
		if err := file.Chmod(mode.Perm()); err != nil {
			return err
		}
	}
	return syncFile(file)
}

func withWritableParent(root *os.Root, rel string, fn func() error) error {
	var ancestors []string
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		ancestors = append(ancestors, dir)
	}
	slices.Reverse(ancestors)
	type restoredPerm struct {
		dir  string
		mode os.FileMode
	}
	var restore []restoredPerm
	for _, dir := range ancestors {
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		perm := info.Mode().Perm()
		if perm&0o700 != 0o700 {
			if f, openErr := root.Open(dir); openErr == nil {
				if cErr := f.Chmod(perm | 0o700); cErr == nil {
					restore = append(restore, restoredPerm{dir: dir, mode: perm})
				}
				_ = f.Close()
			}
		}
	}
	defer func() {
		for i := len(restore) - 1; i >= 0; i-- {
			if f, openErr := root.Open(restore[i].dir); openErr == nil {
				_ = f.Chmod(restore[i].mode)
				_ = syncFile(f)
				_ = f.Close()
			}
		}
	}()
	return fn()
}
