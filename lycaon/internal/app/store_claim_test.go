package app

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/testutil"
)

func claimedTestStore(t *testing.T) (string, *hostlock.Claim) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	claim, err := hostlock.AcquireStore(dbPath)
	testutil.FailErr(t, "acquire store claim", err)
	t.Cleanup(claim.Release)
	testutil.FailErr(t, "create store file", os.WriteFile(dbPath, []byte("store"), 0o600))
	testutil.FailErr(t, "bind store claim", claim.BindStore())
	return dbPath, claim
}

func replaceStoreFile(t *testing.T, dbPath string) {
	t.Helper()
	testutil.FailErr(t, "remove store file", os.Remove(dbPath))
	testutil.FailErr(t, "write another store file", os.WriteFile(dbPath, []byte("other"), 0o600))
}

// The serve loop stops on this signal.
func TestWatchStoreClaimDeliversTheLoss(t *testing.T) {
	prev := storeClaimCheckInterval
	storeClaimCheckInterval = time.Millisecond
	t.Cleanup(func() { storeClaimCheckInterval = prev })
	dbPath, claim := claimedTestStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lost := (&ServeApp{storeClaim: claim}).watchStoreClaim(ctx)

	select {
	case err := <-lost:
		t.Fatalf("an intact claim reported a loss: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	replaceStoreFile(t, dbPath)
	select {
	case err := <-lost:
		if !errors.Is(err, hostlock.ErrStoreClaimLost) {
			t.Fatalf("watch delivered %v, want the claim loss", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a replaced store was never reported")
	}
}

func TestWatchStoreClaimWithoutAClaimNeverFires(t *testing.T) {
	if lost := (&ServeApp{}).watchStoreClaim(context.Background()); lost != nil {
		t.Fatal("an app with no claim must not watch one")
	}
}

// Reconcile stops before it reads the registry once the claim is lost.
func TestReconcileStoreCoupledStorageRefusesWhenTheStoreClaimIsLost(t *testing.T) {
	dbPath, claim := claimedTestStore(t)
	replaceStoreFile(t, dbPath)

	err := reconcileStoreCoupledStorage(&serveBuilder{storage: persistence.Runtime{Claim: claim}}, context.Background())
	if !errors.Is(err, hostlock.ErrStoreClaimLost) {
		t.Fatalf("reconcile should refuse on a lost claim, got %v", err)
	}
}
