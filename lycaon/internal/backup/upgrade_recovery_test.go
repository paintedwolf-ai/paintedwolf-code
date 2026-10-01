package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestUpgradeRecoveryRetainsPreviousCopiesUntilReadiness(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, storeRelPath)
	database := testdbfixture.OpenPath(t, path)
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan source", err)
	opts := CreateOpts{ConfigDir: root, DBPath: path, SQLDB: database, AppVersion: "1.0.0", SchemaUserVersion: db.SchemaVersion}
	var archives []string
	for i, target := range []string{"1.0.1", "1.0.2", "1.0.3"} {
		opts.Now = time.Date(2026, 9, 11, 0, 0, i, 0, time.UTC)
		testutil.FailErr(t, "capture before update", CaptureUpgradeRecovery(t.Context(), opts, plan, target))
		archive, _, err := LatestUpgradeRecovery(t.Context(), root)
		testutil.FailErr(t, "find latest", err)
		archives = append(archives, archive)
		testutil.FailErr(t, "retry same interrupted boot", CaptureUpgradeRecovery(t.Context(), opts, plan, target))
		repeated, _, err := LatestUpgradeRecovery(t.Context(), root)
		testutil.FailErr(t, "find repeated", err)
		if repeated != archive {
			t.Fatal("retry created another recovery copy")
		}
		if i < 2 {
			testutil.FailErr(t, "mark application ready", CompleteUpgradeRecovery(root, target))
		}
	}
	for _, archive := range archives {
		if _, err := os.Stat(archive); err != nil {
			t.Fatalf("failed boot reclaimed prior recovery: %v", err)
		}
	}
	testutil.FailErr(t, "finish successful third boot", CompleteUpgradeRecovery(root, "1.0.3"))
	if _, err := os.Stat(archives[0]); !os.IsNotExist(err) {
		t.Fatalf("oldest completed recovery retained: %v", err)
	}
	for _, archive := range archives[1:] {
		if _, err := os.Stat(archive); err != nil {
			t.Fatalf("recent recovery removed: %v", err)
		}
	}
}

func TestRestoreSuspendsImportedRetentionAndRefreshesIntegrityMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, storeRelPath)
	database := testdbfixture.OpenPath(t, path)
	policy := historyretention.DefaultPolicy()
	policy.Recordings = wire.HistoryRetentionRule{Mode: "max_age", MaxAgeDays: 30}
	raw, err := json.Marshal(policy)
	testutil.FailErr(t, "encode source policy", err)
	testutil.FailErr(t, "write source policy", os.WriteFile(filepath.Join(root, historyretention.PolicyFilename), raw, 0o600))
	archive := filepath.Join(t.TempDir(), "backup.zip")
	_, err = Create(t.Context(), CreateOpts{ConfigDir: root, DBPath: path, SQLDB: database, AppVersion: "1.0.0", SchemaUserVersion: db.SchemaVersion}, archive)
	testutil.FailErr(t, "capture policy", err)
	restored := t.TempDir()
	_, err = Stage(t.Context(), StageOpts{ConfigDir: restored, ArchivePath: archive, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage with suspension", err)
	testutil.FailErr(t, "apply verifies refreshed policy hash", ApplyPending(restored))
	restoredRaw, err := os.ReadFile(filepath.Join(restored, historyretention.PolicyFilename))
	testutil.FailErr(t, "read imported policy", err)
	var imported wire.HistoryRetentionPolicy
	testutil.FailErr(t, "decode imported policy", json.Unmarshal(restoredRaw, &imported))
	if !imported.Suspended || imported.Revision != policy.Revision+1 || imported.Recordings != policy.Recordings {
		t.Fatalf("imported policy did not preserve limits while requiring review: %+v", imported)
	}
	sourceRaw, err := os.ReadFile(filepath.Join(root, historyretention.PolicyFilename))
	testutil.FailErr(t, "read source policy", err)
	if string(sourceRaw) != string(raw) {
		t.Fatal("restore changed source policy")
	}
}

func TestFailedUpgradeRecoveryRemainsDiscoverableAcrossNewTargets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, storeRelPath)
	database := testdbfixture.OpenPath(t, path)
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan recovery source", err)
	opts := CreateOpts{ConfigDir: root, DBPath: path, SQLDB: database, AppVersion: "1.0.0", SchemaUserVersion: db.SchemaVersion, Now: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)}
	testutil.FailErr(t, "capture failed target", CaptureUpgradeRecovery(t.Context(), opts, plan, "1.0.1"))
	failed, _, err := LatestUpgradeRecovery(t.Context(), root)
	testutil.FailErr(t, "find failed target copy", err)
	for i, target := range []string{"1.0.2", "1.0.3", "1.0.4"} {
		opts.Now = opts.Now.Add(time.Hour)
		testutil.FailErr(t, "capture next target", CaptureUpgradeRecovery(t.Context(), opts, plan, target))
		records, err := upgradeRecoveryRecords(filepath.Join(root, db.UpgradeRecoveryDirName))
		testutil.FailErr(t, "list recovery metadata", err)
		found := false
		for _, record := range records {
			if record.Snapshot == filepath.Base(failed) {
				found = true
				if record.ReadyAt != "" {
					t.Fatal("failed upgrade marked ready")
				}
			}
		}
		if !found {
			t.Fatalf("failed recovery lost at target %d", i)
		}
		testutil.FailErr(t, "complete next target", CompleteUpgradeRecovery(root, target))
	}
	if _, err := os.Stat(failed); err != nil {
		t.Fatalf("completed recovery pruning removed failed point: %v", err)
	}
}

func TestAlternateVersionReadinessDetachesPendingWithoutSuccessStamp(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, storeRelPath)
	database := testdbfixture.OpenPath(t, path)
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan recovery source", err)
	testutil.FailErr(t, "capture target", CaptureUpgradeRecovery(t.Context(), CreateOpts{ConfigDir: root, DBPath: path, SQLDB: database, AppVersion: "1.0.0", SchemaUserVersion: db.SchemaVersion}, plan, "1.0.1"))
	testutil.FailErr(t, "ready on restored older application", CompleteUpgradeRecovery(root, "1.0.0"))
	if _, err := os.Stat(filepath.Join(root, db.UpgradeRecoveryDirName, "pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending pointer remains: %v", err)
	}
	_, record, err := LatestUpgradeRecovery(t.Context(), root)
	testutil.FailErr(t, "find detached failed point", err)
	if record.ReadyAt != "" || record.TargetAppVersion != "1.0.1" {
		t.Fatalf("detached recovery was rewritten as successful: %+v", record)
	}
}
