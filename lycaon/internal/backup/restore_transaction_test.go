package backup_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The recovery pre-image includes live journal state from disk.
func TestRecoveryModePreImageCarriesTheLiveJournals(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	store := testdbfixture.OpenPath(t, dbPath)

	archivePath := filepath.Join(t.TempDir(), "snapshot.zip")
	_, err := backup.Create(ctx, backup.CreateOpts{ConfigDir: configDir, DBPath: dbPath, SQLDB: store, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}, archivePath)
	testutil.FailErr(t, "capture recovery", err)

	// Leave a hot journal beside the store.
	_, err = store.ExecContext(ctx, `INSERT INTO store_meta(key, value) VALUES('pre_image_probe','1')
ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
	testutil.FailErr(t, "write store", err)
	_, err = os.Stat(dbPath + "-wal")
	testutil.FailErr(t, "hot wal beside the store", err)

	// Recovery mode has no live database handle.
	result, err := backup.Stage(ctx, backup.StageOpts{
		ConfigDir:     configDir,
		ArchivePath:   archivePath,
		SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage recovery snapshot", err)

	for _, suffix := range db.StoreSidecarSuffixes() {
		live := dbPath + suffix
		if _, statErr := os.Stat(live); statErr != nil {
			continue
		}
		preImage := filepath.Join(result.RecoveryCopyPath, "store.db"+suffix)
		if _, statErr := os.Stat(preImage); statErr != nil {
			t.Fatalf("pre-image is missing the live %s the apply is about to delete", "store.db"+suffix)
		}
	}
}

func TestFailedApplyReachesRecoveryAndReleasesTheNextTransaction(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	store, err := db.Open(dbPath)
	testutil.FailErr(t, "open store", err)

	_, err = backup.StageFreshStart(ctx, backup.FreshStartOpts{
		ConfigDir: configDir, DBPath: dbPath, SQLDB: store, AppVersion: "test",
	})
	testutil.FailErr(t, "stage fresh start", err)
	testutil.FailErr(t, "close store", store.Shutdown(ctx))

	// A non-empty directory blocks deletion of the onboarding latch.
	onboarding := filepath.Join(configDir, filepath.FromSlash(localdata.FirstRunOnboardingRelPath()))
	testutil.FailErr(t, "seed blocked delete", os.MkdirAll(onboarding, 0o700))
	testutil.FailErr(t, "seed blocked delete child",
		os.WriteFile(filepath.Join(onboarding, "child"), []byte("x"), 0o600))

	applyErr := backup.ApplyPending(configDir)
	if applyErr == nil {
		t.Fatal("apply of a blocked fresh start must fail")
	}
	if !errors.Is(applyErr, db.ErrStoreIncompatible) {
		t.Fatalf("apply err = %v, want db.ErrStoreIncompatible so the boot reaches recovery mode", applyErr)
	}

	marker := readMarker(t, configDir)
	if marker.FailedAt == "" {
		t.Fatal("the marker does not record the failed apply, so nothing can replace the stuck transaction")
	}

	// A failed marker permits a replacement transaction.
	testutil.FailErr(t, "clear blocked delete", os.RemoveAll(onboarding))
	live := testdbfixture.OpenPath(t, dbPath)
	result, err := backup.StageFreshStart(ctx, backup.FreshStartOpts{
		ConfigDir: configDir, DBPath: dbPath, SQLDB: live, AppVersion: "test",
	})
	if err != nil {
		t.Fatalf("staging after a failed apply = %v, want the transaction replaced", err)
	}
	if result.SupersededTransaction == "" {
		t.Fatal("the replaced transaction was not reported")
	}
	if _, statErr := os.Stat(result.SupersededTransaction); statErr != nil {
		t.Fatal("the pre-image the failed transaction displaced was discarded")
	}
}

func TestStageStillRefusesAPendingRestart(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	store := testdbfixture.OpenPath(t, dbPath)

	_, err := backup.StageFreshStart(ctx, backup.FreshStartOpts{
		ConfigDir: configDir, DBPath: dbPath, SQLDB: store, AppVersion: "test",
	})
	testutil.FailErr(t, "stage fresh start", err)

	_, err = backup.StageFreshStart(ctx, backup.FreshStartOpts{
		ConfigDir: configDir, DBPath: dbPath, SQLDB: store, AppVersion: "test",
	})
	if !errors.Is(err, backup.ErrPending) {
		t.Fatalf("second stage err = %v, want ErrPending", err)
	}
}

func TestStageReclaimsPreImagesOfCompletedTransactions(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	store := testdbfixture.OpenPath(t, dbPath)

	stale := filepath.Join(configDir, localdata.RestorePreImageDirPrefix+"-11111111-1111-1111-1111-111111111111")
	testutil.FailErr(t, "seed stale pre-image", os.MkdirAll(stale, 0o700))
	testutil.FailErr(t, "seed stale pre-image file",
		os.WriteFile(filepath.Join(stale, "store.db"), []byte("old history"), 0o600))

	result, err := backup.StageFreshStart(ctx, backup.FreshStartOpts{
		ConfigDir: configDir, DBPath: dbPath, SQLDB: store, AppVersion: "test",
	})
	testutil.FailErr(t, "stage fresh start", err)

	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Fatal("a pre-image of a completed transaction survived the next transaction")
	}
	if len(result.ReclaimedPreImages) != 1 || result.ReclaimedPreImages[0].Path != stale {
		t.Fatalf("reclaimed = %v, want the stale pre-image reported", result.ReclaimedPreImages)
	}
	if _, statErr := os.Stat(result.RecoveryCopyPath); statErr != nil {
		t.Fatal("the new transaction's own pre-image was reclaimed")
	}
}

func readMarker(t *testing.T, configDir string) backup.PendingMarker {
	t.Helper()
	raw, err := os.ReadFile(backup.PendingMarkerPath(configDir)) // #nosec G304 -- temporary fixture path
	testutil.FailErr(t, "read pending marker", err)
	var marker backup.PendingMarker
	testutil.FailErr(t, "parse pending marker", json.Unmarshal(raw, &marker))
	return marker
}
