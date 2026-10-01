package hostlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStoreInstanceLockRefusesSecondHolder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")

	claim, err := AcquireStore(dbPath)
	testutil.FailErr(t, "first acquire", err)

	second, err := AcquireStore(dbPath)
	if err == nil {
		second.Release()
		t.Fatal("second engine acquired a store already being served")
	}
	// The shell branches on the sentinel.
	if !errors.Is(err, ErrStoreInstanceLocked) {
		t.Fatalf("error should be ErrStoreInstanceLocked, got %v", err)
	}
	// The message names the store.
	if !strings.Contains(err.Error(), dbPath) {
		t.Fatalf("error should name the store path, got %v", err)
	}

	claim.Release()

	// Releasing hands the store back.
	again, err := AcquireStore(dbPath)
	testutil.FailErr(t, "acquire after release", err)
	again.Release()
}

// The refusal names the holder's pid so a windowless engine can be found.
func TestStoreInstanceLockNamesTheHolder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")

	claim, err := AcquireStore(dbPath)
	testutil.FailErr(t, "first acquire", err)
	defer claim.Release()

	_, err = AcquireStore(dbPath)
	if err == nil {
		t.Fatal("second engine acquired a store already being served")
	}
	var locked *StoreLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error should be a *StoreLockedError, got %T", err)
	}
	if locked.HolderPID != os.Getpid() {
		t.Fatalf("HolderPID = %d want this process %d", locked.HolderPID, os.Getpid())
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("error should name the holder pid, got %v", err)
	}
}

// The refusal does not depend on the pid stamp.
func TestStoreInstanceLockRefusesWithoutAStamp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")

	claim, err := AcquireStore(dbPath)
	testutil.FailErr(t, "first acquire", err)
	defer claim.Release()

	lockPath, err := storeLockPath(dbPath)
	testutil.FailErr(t, "lock path", err)
	testutil.FailErr(t, "remove stamp", os.Remove(holderPIDPath(lockPath)))

	_, err = AcquireStore(dbPath)
	if !errors.Is(err, ErrStoreInstanceLocked) {
		t.Fatalf("error should be ErrStoreInstanceLocked, got %v", err)
	}
	var locked *StoreLockedError
	if !errors.As(err, &locked) || locked.HolderPID != 0 {
		t.Fatalf("HolderPID should be 0 with no stamp, got %v", err)
	}
	if strings.Contains(err.Error(), "pid ") {
		t.Fatalf("message should not claim a pid it does not have, got %v", err)
	}
}

// Release clears the stamp.
func TestStoreInstanceLockClearsStampOnRelease(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")

	claim, err := AcquireStore(dbPath)
	testutil.FailErr(t, "acquire", err)
	lockPath, err := storeLockPath(dbPath)
	testutil.FailErr(t, "lock path", err)
	if _, err := os.Stat(holderPIDPath(lockPath)); err != nil {
		t.Fatalf("holder stamp missing while lock is held: %v", err)
	}

	claim.Release()

	if _, err := os.Stat(holderPIDPath(lockPath)); !os.IsNotExist(err) {
		t.Fatalf("holder stamp survived release: %v", err)
	}
}

func TestStoreInstanceLockIsPerStore(t *testing.T) {
	dir := t.TempDir()

	claimA, err := AcquireStore(filepath.Join(dir, "a.db"))
	testutil.FailErr(t, "acquire a", err)
	defer claimA.Release()

	// Different stores do not contend.
	claimB, err := AcquireStore(filepath.Join(dir, "b.db"))
	testutil.FailErr(t, "acquire b", err)
	claimB.Release()
}

func TestStoreInstanceLockPathStaysOutOfTheStoreDir(t *testing.T) {
	dir := t.TempDir()
	path, err := storeLockPath(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "lock path", err)

	// The config root holds only inventoried files; locks live in locks/.
	if got := filepath.Base(filepath.Dir(path)); got != "locks" {
		t.Fatalf("lock parent dir = %q want locks", got)
	}
	if !strings.HasSuffix(path, ".engine.lock") {
		t.Fatalf("lock path = %q want a .engine.lock suffix", path)
	}
	// The stamp is a sibling because a Windows byte-range lock blocks reads.
	if stamp := holderPIDPath(path); stamp == path || !strings.HasSuffix(stamp, ".engine.pid") {
		t.Fatalf("holder stamp path = %q want a distinct .engine.pid sibling", stamp)
	}
}

func claimedStore(t *testing.T, dir string) (string, *Claim) {
	t.Helper()
	dbPath := filepath.Join(dir, "store.db")
	claim, err := AcquireStore(dbPath)
	testutil.FailErr(t, "acquire", err)
	testutil.FailErr(t, "create store", os.WriteFile(dbPath, []byte("store"), 0o600))
	testutil.FailErr(t, "bind store", claim.BindStore())
	t.Cleanup(claim.Release)
	return dbPath, claim
}

func TestClaimVerifiesWhileTheStorePathIsIntact(t *testing.T) {
	_, claim := claimedStore(t, t.TempDir())
	testutil.FailErr(t, "verify intact claim", claim.Verify())
}

// A data directory renamed aside and reused by a new engine at the same path
// leaves the old engine holding a store its paths no longer reach.
func TestClaimIsLostWhenTheDataDirectoryIsReplacedByAnotherEngine(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "data")
	testutil.FailErr(t, "create data dir", os.Mkdir(dir, 0o700))
	_, stale := claimedStore(t, dir)

	testutil.FailErr(t, "rename data dir aside", os.Rename(dir, filepath.Join(parent, "data-old")))
	testutil.FailErr(t, "recreate data dir", os.Mkdir(dir, 0o700))
	fresh, err := AcquireStore(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "new engine acquires the reused path", err)
	defer fresh.Release()

	err = stale.Verify()
	var lost *ClaimLostError
	if !errors.As(err, &lost) || !errors.Is(err, ErrStoreClaimLost) {
		t.Fatalf("stale claim should be lost, got %v", err)
	}
	testutil.FailErr(t, "the new engine's claim is unaffected", fresh.Verify())

	// The stamp at the reused path belongs to the new engine.
	stale.Release()
	lockPath, err := storeLockPath(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "lock path", err)
	if _, err := os.Stat(holderPIDPath(lockPath)); err != nil {
		t.Fatalf("stale release removed the new engine's holder stamp: %v", err)
	}
}

func TestClaimIsLostWhenTheDataDirectoryIsRenamedAway(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "data")
	testutil.FailErr(t, "create data dir", os.Mkdir(dir, 0o700))
	_, claim := claimedStore(t, dir)

	testutil.FailErr(t, "rename data dir away", os.Rename(dir, filepath.Join(parent, "data-old")))

	var lost *ClaimLostError
	if err := claim.Verify(); !errors.As(err, &lost) {
		t.Fatalf("claim should be lost, got %v", err)
	}
}

func TestClaimIsLostWhenTheStoreFileIsReplaced(t *testing.T) {
	dbPath, claim := claimedStore(t, t.TempDir())

	testutil.FailErr(t, "remove store", os.Remove(dbPath))
	testutil.FailErr(t, "write another store", os.WriteFile(dbPath, []byte("other"), 0o600))

	var lost *ClaimLostError
	if err := claim.Verify(); !errors.As(err, &lost) {
		t.Fatalf("claim should be lost, got %v", err)
	}
}

// An uninspectable path is not a loss, but nothing is proven either.
func TestClaimDistinguishesUnreadablePathsFromLoss(t *testing.T) {
	dir := t.TempDir()
	_, claim := claimedStore(t, dir)
	testutil.FailErr(t, "hide the lock directory", os.Chmod(filepath.Join(dir, "locks"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locks"), 0o700) })

	err := claim.Verify()
	var lost *ClaimLostError
	if err == nil || errors.As(err, &lost) {
		t.Fatalf("an unreadable lock path is an inspection failure, not a loss: %v", err)
	}
}

func TestLostReturnsWhenTheStoreClaimIsReplaced(t *testing.T) {
	dbPath, claim := claimedStore(t, t.TempDir())
	testutil.FailErr(t, "remove store", os.Remove(dbPath))
	testutil.FailErr(t, "write another store", os.WriteFile(dbPath, []byte("other"), 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := claim.Lost(ctx, time.Millisecond)
	if !errors.Is(err, ErrStoreClaimLost) {
		t.Fatalf("Lost should report the loss, got %v", err)
	}
}

func TestLostStopsWithItsContext(t *testing.T) {
	_, claim := claimedStore(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := claim.Lost(ctx, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lost should return the context error, got %v", err)
	}
}
