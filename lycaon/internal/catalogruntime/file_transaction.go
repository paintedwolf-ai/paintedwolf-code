package catalogruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// FileUpdate changes one durable catalog generation and its runtime projection.
// Publication or validation failure restores both.
type FileUpdate struct {
	LockRoot string
	Target   string
	Mode     os.FileMode
	DirMode  os.FileMode
	Apply    func() error
	Publish  func() error
	Validate func() error
}

type fileLockEntry struct {
	semaphore chan struct{}
	refs      int
}

var catalogFileLocks = struct {
	sync.Mutex
	entries map[string]*fileLockEntry
}{entries: map[string]*fileLockEntry{}}

// UpdateFile applies and publishes one serialized catalog generation.
func UpdateFile(ctx context.Context, update FileUpdate) error {
	if ctx == nil {
		return fmt.Errorf("catalog transaction context is required")
	}
	target, lockRoot, err := validateFileUpdate(update)
	if err != nil {
		return err
	}
	releaseProcess, err := acquireCatalogProcessLock(ctx, target)
	if err != nil {
		return err
	}
	defer releaseProcess()
	releaseFile, err := acquireCatalogFileLock(ctx, lockRoot, target)
	if err != nil {
		return err
	}
	defer releaseFile()

	before, existed, err := readCatalogFile(target)
	if err != nil {
		return fmt.Errorf("catalog transaction snapshot: %w", err)
	}
	if err := update.Apply(); err != nil {
		if rollbackErr := restoreCatalogFile(target, before, existed, update.Mode, update.DirMode); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("catalog transaction rollback after write failure: %w", rollbackErr))
		}
		return err
	}
	if update.Publish != nil {
		if err := update.Publish(); err != nil {
			return rollbackPublishedUpdate(err, update, target, before, existed)
		}
	}
	if update.Validate != nil {
		if err := update.Validate(); err != nil {
			return rollbackPublishedUpdate(err, update, target, before, existed)
		}
	}
	return nil
}

func rollbackPublishedUpdate(cause error, update FileUpdate, target string, before []byte, existed bool) error {
	rollbackErr := restoreCatalogFile(target, before, existed, update.Mode, update.DirMode)
	if rollbackErr == nil && update.Publish != nil {
		rollbackErr = update.Publish()
	}
	if rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf("catalog transaction rollback publication: %w", rollbackErr))
	}
	return cause
}

func validateFileUpdate(update FileUpdate) (target, lockRoot string, err error) {
	if update.Apply == nil {
		return "", "", fmt.Errorf("catalog transaction apply function is required")
	}
	target, lockRoot, err = cleanTransactionPaths(update.Target, update.LockRoot)
	if err != nil {
		return "", "", err
	}
	if update.Mode.Perm() == 0 {
		return "", "", fmt.Errorf("catalog transaction file mode is required")
	}
	return target, lockRoot, nil
}

func cleanTransactionPaths(target, lockRoot string) (string, string, error) {
	absTarget, err := filepath.Abs(target)
	if err != nil || target == "" {
		return "", "", fmt.Errorf("catalog transaction target is required")
	}
	absLockRoot, err := filepath.Abs(lockRoot)
	if err != nil || lockRoot == "" {
		return "", "", fmt.Errorf("catalog transaction lock root is required")
	}
	return filepath.Clean(absTarget), filepath.Clean(absLockRoot), nil
}

// ReadFile reads target's current bytes while holding the same process and
// cross-process locks UpdateFile holds during Apply/Publish/Validate, so a
// reader can never observe a write mid-apply or a row about to be rolled
// back. existed is false when target does not exist (data is nil then).
func ReadFile(ctx context.Context, lockRoot, target string) (data []byte, existed bool, err error) {
	if ctx == nil {
		return nil, false, fmt.Errorf("catalog transaction context is required")
	}
	target, lockRoot, err = cleanTransactionPaths(target, lockRoot)
	if err != nil {
		return nil, false, err
	}
	releaseProcess, err := acquireCatalogProcessLock(ctx, target)
	if err != nil {
		return nil, false, err
	}
	defer releaseProcess()
	releaseFile, err := acquireCatalogFileLock(ctx, lockRoot, target)
	if err != nil {
		return nil, false, err
	}
	defer releaseFile()
	return readCatalogFile(target)
}

func acquireCatalogProcessLock(ctx context.Context, target string) (func(), error) {
	catalogFileLocks.Lock()
	entry := catalogFileLocks.entries[target]
	if entry == nil {
		entry = &fileLockEntry{semaphore: make(chan struct{}, 1)}
		catalogFileLocks.entries[target] = entry
	}
	entry.refs++
	catalogFileLocks.Unlock()
	releaseRef := func() {
		catalogFileLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(catalogFileLocks.entries, target)
		}
		catalogFileLocks.Unlock()
	}
	select {
	case entry.semaphore <- struct{}{}:
		return func() {
			<-entry.semaphore
			releaseRef()
		}, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

func acquireCatalogFileLock(ctx context.Context, lockRoot, target string) (func(), error) {
	sum := sha256.Sum256([]byte(target))
	path := filepath.Join(lockRoot, "locks", hex.EncodeToString(sum[:16])+".catalog.lock")
	file, err := filelock.Open(path)
	if err != nil {
		return nil, fmt.Errorf("catalog transaction lock: %w", err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, lockErr := filelock.TryExclusive(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("catalog transaction lock: %w", lockErr)
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

func readCatalogFile(target string) ([]byte, bool, error) {
	file, err := fseffect.OpenRead(fseffect.PathLocation(target))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(file)
	return body, true, err
}

func restoreCatalogFile(target string, before []byte, existed bool, mode, dirMode os.FileMode) error {
	if !existed {
		err := fseffect.Remove(fseffect.RemoveRequest{Location: fseffect.PathLocation(target)})
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(target),
		Source:   bytes.NewReader(before),
		Mode:     mode,
		DirMode:  dirMode,
	})
	return err
}
