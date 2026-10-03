// Package hostlock manages the single-writer claim on an engine store.
package hostlock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
)

// ErrStoreInstanceLocked reports that another engine already serves this store.
// The shell distinguishes it from other startup failures: retrying cannot help.
var ErrStoreInstanceLocked = errors.New("store already served by another engine")

// ExitCodeStoreInstanceLocked is `serve`'s exit status when the store lock is
// held. The shell mirrors it as STORE_INSTANCE_LOCKED_EXIT in sidecar/startup.rs,
// and a test there fails on drift.
const ExitCodeStoreInstanceLocked = 69

// StoreLockedError is a refused claim, naming the store and the holder's pid
// when it stamped one.
type StoreLockedError struct {
	StorePath string
	// HolderPID is 0 when no stamp could be read.
	HolderPID int
}

func (e *StoreLockedError) Error() string {
	who := ""
	if e.HolderPID > 0 {
		who = fmt.Sprintf(" (pid %d)", e.HolderPID)
	}
	return fmt.Sprintf(
		"%s%s: %s — quit the running app (or stop the other `serve`) and try again",
		ErrStoreInstanceLocked.Error(), who, e.StorePath,
	)
}

func (e *StoreLockedError) Unwrap() error { return ErrStoreInstanceLocked }

// Guard proves an engine still owns the store it is about to change. A pass
// that deletes on the strength of database rows refuses when it fails.
type Guard interface {
	Verify() error
}

// ErrStoreClaimLost reports that the store path no longer names the files this
// engine opened.
var ErrStoreClaimLost = errors.New("store claim lost")

// ClaimLostError names the store and what changed under it.
type ClaimLostError struct {
	StorePath string
	Reason    string
}

func (e *ClaimLostError) Error() string {
	return fmt.Sprintf(
		"%s: %s: %s — this engine holds a store that is no longer at that path; restart the app",
		ErrStoreClaimLost.Error(), e.StorePath, e.Reason,
	)
}

func (e *ClaimLostError) Unwrap() error { return ErrStoreClaimLost }

// Claim is one engine's hold on one store.
type Claim struct {
	dbPath   string
	lockPath string
	lock     *os.File
	lockInfo os.FileInfo

	mu sync.Mutex
	// store stays open so its inode cannot be reused by a replacement,
	// which would otherwise pass as the same file on some filesystems.
	store     *os.File
	storeInfo os.FileInfo
}

// AcquireStore takes the exclusive right to serve one store.
//
// Two engines on one store duplicate pollers, race session state, and spend on
// LLM calls nobody sees; WAL prevents file corruption, not that. The lock is
// keyed on the store path and, as an OS advisory lock, releases on crash.
func AcquireStore(dbPath string) (*Claim, error) {
	path, err := storeLockPath(dbPath)
	if err != nil {
		return nil, err
	}
	f, err := filelock.Open(path)
	if err != nil {
		return nil, fmt.Errorf("engine instance lock: %w", err)
	}
	held, err := filelock.TryExclusive(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("engine instance lock: %w", err)
	}
	if !held {
		_ = f.Close()
		return nil, &StoreLockedError{StorePath: dbPath, HolderPID: readHolderPID(path)}
	}
	info, err := f.Stat()
	if err != nil {
		_ = filelock.Unlock(f)
		_ = f.Close()
		return nil, fmt.Errorf("engine instance lock: %w", err)
	}
	writeHolderPID(path, os.Getpid())
	return &Claim{dbPath: dbPath, lockPath: path, lock: f, lockInfo: info}, nil
}

// BindStore records the identity of the store file just opened. Call it right
// after opening.
func (c *Claim) BindStore() error {
	store, err := os.Open(c.dbPath)
	if err != nil {
		return fmt.Errorf("bind store claim: %w", err)
	}
	info, err := store.Stat()
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("bind store claim: %w", err)
	}
	c.mu.Lock()
	previous := c.store
	c.store, c.storeInfo = store, info
	c.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

// Verify reports whether the store path still names the lock and store files
// this claim holds. A *ClaimLostError is permanent; any other error means the
// paths could not be inspected.
func (c *Claim) Verify() error {
	if c == nil {
		return errors.New("store claim: not held")
	}
	if err := c.verifyFile(c.lockPath, c.lockInfo, "engine lock file"); err != nil {
		return err
	}
	c.mu.Lock()
	bound := c.storeInfo
	c.mu.Unlock()
	if bound == nil {
		return nil
	}
	return c.verifyFile(c.dbPath, bound, "store file")
}

func (c *Claim) verifyFile(path string, held os.FileInfo, what string) error {
	current, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &ClaimLostError{StorePath: c.dbPath, Reason: what + " is gone"}
	case err != nil:
		return fmt.Errorf("verify store claim: %w", err)
	case !os.SameFile(held, current):
		return &ClaimLostError{StorePath: c.dbPath, Reason: what + " was replaced"}
	}
	return nil
}

// Lost blocks until the claim is lost or ctx ends. Inspection failures are
// retried on the next tick.
func (c *Claim) Lost(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var lost *ClaimLostError
			if err := c.Verify(); errors.As(err, &lost) {
				return lost
			}
		}
	}
}

// Release gives the store back. It clears the holder stamp only while the claim
// is intact; after a loss the stamp belongs to the new holder.
func (c *Claim) Release() {
	if c == nil {
		return
	}
	if c.Verify() == nil {
		clearHolderPID(c.lockPath)
	}
	_ = filelock.Unlock(c.lock)
	_ = c.lock.Close()
	c.mu.Lock()
	store := c.store
	c.store = nil
	c.mu.Unlock()
	if store != nil {
		_ = store.Close()
	}
}

// storeLockPath is {configdir}/locks/{hash}.engine.lock. It sits in locks/ so
// the config root holds only files the backup and clear registries know about,
// and so a restore never replaces the lock itself.
func storeLockPath(dbPath string) (string, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		abs = dbPath
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	dir := filepath.Join(filepath.Dir(abs), "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("engine instance lock dir: %w", err)
	}
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".engine.lock"), nil
}

// holderPIDPath sits beside the lock, not inside it: the Windows byte-range
// lock refuses reads of the range covered by its holder.
func holderPIDPath(lockPath string) string {
	return strings.TrimSuffix(lockPath, ".lock") + ".pid"
}

// writeHolderPID records who holds the lock. Best-effort: a missing stamp costs
// a later error its pid, which is not worth failing a boot over.
func writeHolderPID(lockPath string, pid int) {
	_ = os.WriteFile(holderPIDPath(lockPath), []byte(strconv.Itoa(pid)), 0o600)
}

func clearHolderPID(lockPath string) {
	_ = os.Remove(holderPIDPath(lockPath))
}

// readHolderPID reads the holder's stamp. Only called after a failed acquire,
// so the lock is held; a crashed holder's stamp can still be read in the window
// before the next engine overwrites it, showing a dead pid.
func readHolderPID(lockPath string) int {
	raw, err := os.ReadFile(holderPIDPath(lockPath)) // #nosec G304 -- derived from the lock path
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
