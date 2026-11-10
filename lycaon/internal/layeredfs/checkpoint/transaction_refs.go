package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// prepareRefs records each root's ref undo. Only the built-in namespaced importer runs, because its
// refs can be restored by compare-and-swap.
func (txn *Transaction) prepareRefs(m Manifest, targetRoots map[string]string) ([]refUndo, error) {
	if !m.PrivateGit.HasRefs() {
		return nil, nil
	}
	if err := ValidateGitRefSegment(m.WorkerID, "worker"); err != nil {
		return nil, err
	}
	if err := ValidateGitRefSegment(m.CheckpointID, "checkpoint"); err != nil {
		return nil, err
	}
	var undo []refUndo
	if primaryID := selectPrimaryRootID(targetRoots); len(m.PrivateGit.Refs) > 0 && repositoryPresent(targetRoots[primaryID]) {
		txn.journal.Checkpoint.PrimaryRootID = primaryID
		refs, err := txn.rootRefUndo(m, primaryID, targetRoots[primaryID], m.PrivateGit.Refs)
		if err != nil {
			return nil, err
		}
		undo = append(undo, refs...)
	}
	for _, rootID := range slices.Sorted(maps.Keys(m.PrivateGit.Roots)) {
		dir := strings.TrimSpace(targetRoots[rootID])
		if dir == "" {
			return nil, fmt.Errorf("checkpoint: git refs name unknown root %q", rootID)
		}
		if !repositoryPresent(dir) {
			continue
		}
		refs, err := txn.rootRefUndo(m, rootID, dir, m.PrivateGit.Roots[rootID].Refs)
		if err != nil {
			return nil, err
		}
		undo = append(undo, refs...)
	}
	return undo, nil
}

func (txn *Transaction) rootRefUndo(m Manifest, rootID, dir string, named map[string]string) ([]refUndo, error) {
	gitDir, common, err := ResolveGitDirs(dir, txn.cfg.AuthorizedGitDirs...)
	if err != nil {
		return nil, err
	}
	_, lycaonErr := os.Lstat(filepath.Join(common, "refs", "lycaon"))
	noLooseLycaon := errors.Is(lycaonErr, os.ErrNotExist) && filepath.Clean(gitDir) == filepath.Clean(common)
	packed := loadPackedRefsMap(common, gitDir)
	var refs []refUndo
	for _, name := range slices.Sorted(maps.Keys(named)) {
		suffix, err := ValidateGitRefName(name)
		if err != nil {
			return nil, err
		}
		oid := strings.ToLower(strings.TrimSpace(named[name]))
		if !isHexOID(oid) || isAllZeroOID(oid) {
			return nil, fmt.Errorf("checkpoint: invalid ref object")
		}
		for _, ref := range []string{"refs/lycaon/workers/" + m.WorkerID + "/" + suffix, "refs/lycaon/checkpoints/" + m.CheckpointID + "/" + suffix} {
			old := packed[ref]
			if !noLooseLycaon {
				old = resolveExistingGitRefWithPacked(common, gitDir, ref, packed)
			}
			if old != "" && old != oid {
				return nil, ErrCheckpointRefConflict
			}
			refs = append(refs, refUndo{RootID: rootID, Name: ref, Before: old, After: oid})
		}
	}
	return refs, nil
}

// rollbackRefs restores each root's refs in its own repository.
func (txn *Transaction) rollbackRefs(ctx context.Context, refs []refUndo) error {
	for start := 0; start < len(refs); {
		end := start
		for end < len(refs) && refs[end].RootID == refs[start].RootID {
			end++
		}
		if err := txn.rollbackRootRefs(ctx, refs[start].RootID, refs[start:end]); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func (txn *Transaction) rollbackRootRefs(ctx context.Context, rootID string, refs []refUndo) error {
	primary := txn.targets.roots[rootID]
	gitDir, common, err := ResolveGitDirs(primary, txn.cfg.AuthorizedGitDirs...)
	if err != nil {
		return err
	}
	sorted := slices.Clone(refs)
	slices.SortFunc(sorted, func(a, b refUndo) int { return strings.Compare(a.Name, b.Name) })
	type rollbackLock struct {
		ref      refUndo
		refPath  string
		lockPath string
		file     *os.File
	}
	locks := make([]rollbackLock, 0, len(sorted))
	cleanup := func() {
		for _, lk := range locks {
			if lk.file != nil {
				_ = lk.file.Close()
			}
			if lk.lockPath != "" {
				_ = os.Remove(lk.lockPath)
			}
		}
	}
	for _, ref := range sorted {
		if err := ctx.Err(); err != nil {
			cleanup()
			return err
		}
		if _, err := ValidateGitRefName(ref.Name); err != nil {
			cleanup()
			return err
		}
		if !strings.HasPrefix(ref.Name, "refs/lycaon/") || !isHexOID(ref.After) || (ref.Before != "" && !isHexOID(ref.Before)) {
			cleanup()
			return ErrInvalidManifest
		}
		refPath := filepath.Join(common, filepath.FromSlash(ref.Name))
		if err := os.MkdirAll(filepath.Dir(refPath), 0o750); err != nil {
			cleanup()
			return err
		}
		lockPath := refPath + ".lock"
		f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			cleanup()
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%w: ref lock busy for %s", ErrCheckpointRefConflict, ref.Name)
			}
			return err
		}
		locks = append(locks, rollbackLock{ref: ref, refPath: refPath, lockPath: lockPath, file: f})
	}
	packed := loadPackedRefsMap(common, gitDir)
	for _, lk := range locks {
		current := resolveExistingGitRefWithPacked(common, gitDir, lk.ref.Name, packed)
		if current != lk.ref.Before && current != lk.ref.After {
			cleanup()
			return fmt.Errorf("%w: imported ref %s diverged", ErrCheckpointRefConflict, lk.ref.Name)
		}
	}
	syncedDirs := make(map[string]bool, len(locks))
	for i := range locks {
		lk := &locks[i]
		current := resolveExistingGitRefWithPacked(common, gitDir, lk.ref.Name, packed)
		if current == lk.ref.Before {
			_ = lk.file.Close()
			lk.file = nil
			_ = os.Remove(lk.lockPath)
			lk.lockPath = ""
			continue
		}
		dir := filepath.Dir(lk.refPath)
		if lk.ref.Before == "" {
			_ = lk.file.Close()
			lk.file = nil
			if err := os.Remove(lk.refPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanup()
				return err
			}
			_ = os.Remove(lk.lockPath)
			lk.lockPath = ""
		} else {
			if _, err := lk.file.WriteString(lk.ref.Before + "\n"); err != nil {
				cleanup()
				return err
			}
			if err := errors.Join(syncObjectFileFn(lk.file), closeObjectFileFn(lk.file)); err != nil {
				lk.file = nil
				cleanup()
				return err
			}
			lk.file = nil
			if err := os.Rename(lk.lockPath, lk.refPath); err != nil {
				cleanup()
				return err
			}
			lk.lockPath = ""
		}
		if !syncedDirs[dir] {
			syncedDirs[dir] = true
			if err := syncDirIfPresent(dir); err != nil {
				cleanup()
				return err
			}
		}
	}
	return nil
}
