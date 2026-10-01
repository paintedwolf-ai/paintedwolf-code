package workspace

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// branchLastUsedFile lives in the .meta sibling so the stamp outlives the tree.
const branchLastUsedFile = "LAST_USED"

const branchUseLockSuffix = ".use.lock"

// ErrBranchInUse reports a branch tree some reader currently holds.
var ErrBranchInUse = errors.New("worker branch is in use")

// TouchBranchUse records that something read or extended the branch tree now.
func TouchBranchUse(branchRoot string) {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" {
		return
	}
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		return
	}
	path := filepath.Join(metaDir, branchLastUsedFile)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		_, _ = fseffect.Replace(fseffect.ReplaceRequest{
			Location: fseffect.PathLocation(path), Source: strings.NewReader(""), Mode: 0o600, DirMode: 0o700,
		})
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// BranchLastUsed reads the use stamp or the unstamped tree's modification time.
func BranchLastUsed(branchRoot string) time.Time {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" {
		return time.Time{}
	}
	if info, err := os.Stat(filepath.Join(enginepaths.MetaDirForBranchRoot(branchRoot), branchLastUsedFile)); err == nil {
		return info.ModTime()
	}
	if info, err := os.Stat(branchRoot); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// branchUseLocks are the in-process half of the use lease, keyed by tree root.
var branchUseLocks = struct {
	mu     sync.Mutex
	byRoot map[string]*sync.RWMutex
}{byRoot: map[string]*sync.RWMutex{}}

func branchUseLock(branchRoot string) *sync.RWMutex {
	branchUseLocks.mu.Lock()
	defer branchUseLocks.mu.Unlock()
	lock, ok := branchUseLocks.byRoot[branchRoot]
	if !ok {
		lock = &sync.RWMutex{}
		branchUseLocks.byRoot[branchRoot] = lock
	}
	return lock
}

// AcquireBranchUse takes a shared lease on the branch tree. Eviction cannot
// proceed while any lease is held, so readers hold one for as long as they
// read. The wait is bounded only by ctx.
func AcquireBranchUse(ctx context.Context, branchRoot string) (release func(), err error) {
	branchRoot, err = cleanBranchRoot(branchRoot)
	if err != nil {
		return nil, err
	}
	inProcess := branchUseLock(branchRoot)
	inProcess.RLock()
	file, err := filelock.Open(branchRoot + branchUseLockSuffix)
	if err != nil {
		inProcess.RUnlock()
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, lockErr := filelock.TryShared(file)
		if lockErr != nil {
			_ = file.Close()
			inProcess.RUnlock()
			return nil, lockErr
		}
		if held {
			return func() {
				_ = filelock.Unlock(file)
				_ = file.Close()
				inProcess.RUnlock()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			inProcess.RUnlock()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// tryExclusiveBranchUse takes the lease exclusively without waiting; a held
// lease means the tree is in use and eviction steps aside.
func tryExclusiveBranchUse(branchRoot string) (release func(), err error) {
	inProcess := branchUseLock(branchRoot)
	if !inProcess.TryLock() {
		return nil, ErrBranchInUse
	}
	file, err := filelock.Open(branchRoot + branchUseLockSuffix)
	if err != nil {
		inProcess.Unlock()
		return nil, err
	}
	held, err := filelock.TryExclusive(file)
	if err != nil {
		_ = file.Close()
		inProcess.Unlock()
		return nil, err
	}
	if !held {
		_ = file.Close()
		inProcess.Unlock()
		return nil, ErrBranchInUse
	}
	return func() {
		_ = filelock.Unlock(file)
		_ = file.Close()
		inProcess.Unlock()
	}, nil
}

// removeBranchLocks drops the lock files that sit beside a branch tree.
func removeBranchLocks(branchRoot string) {
	_ = os.Remove(branchRoot + branchUseLockSuffix)
	_ = os.Remove(branchRoot + provisionLockSuffix)
}

// BranchTreePresent reports whether the branch tree is on disk and, when it
// carries an eviction record, that the record says the snapshot is complete.
// A tree with no record was never evicted and reads as present.
func BranchTreePresent(branchRoot string) bool {
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return false
	}
	info, err := os.Lstat(branchRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot))
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return meta.SnapshotComplete
}
