package contentblob

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOrphanSweepRecoversInterruptedPublicationOnlyAfterLeaseEnds(t *testing.T) {
	root := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	store := StoreFor(root, testdbseed.DefaultProjectID)
	sha, _, _, err := Write(store, []byte("interrupted before reference commit"))
	testutil.FailErr(t, "publish orphan", err)
	rel, err := RelPath(sha)
	testutil.FailErr(t, "orphan path", err)
	abs := filepath.Join(store.Root, rel)
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age interrupted publication", os.Chtimes(abs, old, old))
	deps := GCDeps{Database: database, DataDir: root, Guard: testdbfixture.ClaimStore(t, filepath.Join(root, "store.db"))}
	var cursor orphanCursor
	defer cursor.close()
	release := bloblifecycle.AcquirePublication(root)
	testutil.FailErr(t, "skip active publication", cursor.batch(t.Context(), deps))
	_, err = os.Stat(abs)
	release()
	testutil.FailErr(t, "leased file remains", err)
	for range 258 {
		testutil.FailErr(t, "advance bounded orphan cursor", cursor.batch(t.Context(), deps))
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("unowned publication was not reclaimed: %v", err)
	}
}

// A lost claim stops the sweep.
func TestOrphanSweepRefusesWhenTheStoreClaimIsLost(t *testing.T) {
	root := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	store := StoreFor(root, testdbseed.DefaultProjectID)
	sha, _, _, err := Write(store, []byte("body a foreign database has no row for"))
	testutil.FailErr(t, "write body", err)
	rel, err := RelPath(sha)
	testutil.FailErr(t, "body path", err)
	abs := filepath.Join(store.Root, rel)
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age body", os.Chtimes(abs, old, old))

	deps := GCDeps{Database: database, DataDir: root, Guard: lostGuard{}}
	var cursor orphanCursor
	defer cursor.close()
	for range 258 {
		if err := cursor.batch(t.Context(), deps); !errors.Is(err, hostlock.ErrStoreClaimLost) {
			t.Fatalf("sweep should refuse on a lost claim, got %v", err)
		}
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("a refused sweep deleted the file: %v", err)
	}
}

type lostGuard struct{}

func (lostGuard) Verify() error {
	return &hostlock.ClaimLostError{StorePath: "store.db", Reason: "store file was replaced"}
}
