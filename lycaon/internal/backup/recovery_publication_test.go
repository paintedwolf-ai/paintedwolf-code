package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryPruneFailureIsOptionalButMetadataFailureIsRequired(t *testing.T) {
	root := t.TempDir()
	blockedRoot := filepath.Join(root, "not-a-directory")
	testutil.FailErr(t, "block recovery directory", os.WriteFile(blockedRoot, []byte("occupied"), 0o600))
	records := []UpgradeRecovery{
		{Snapshot: uuid.NewString(), ReadyAt: "ready"},
		{Snapshot: uuid.NewString(), ReadyAt: "ready"},
		{Snapshot: uuid.NewString(), ReadyAt: "ready"},
	}
	var pruning *RecoveryPruneError
	if err := pruneReadyRecoveries(blockedRoot, records); !errors.As(err, &pruning) {
		t.Fatalf("old ready snapshot cleanup error=%v, want optional pruning error", err)
	}
	if err := completePublishedRecoveries(blockedRoot); err == nil || errors.As(err, &pruning) {
		t.Fatalf("required metadata error=%v, want ordinary startup failure", err)
	}
}

func TestRecoveryPublicationCrashGapRetainsAndReusesSnapshot(t *testing.T) {
	root := t.TempDir()
	opts, plan, record := publishFixtureRecoveryWithoutPointer(t, root)
	recoveryRoot := filepath.Join(root, db.UpgradeRecoveryDirName)
	// This is the persisted state after atomic directory publication and before pending.json.
	if _, err := os.Stat(filepath.Join(recoveryRoot, "pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending pointer unexpectedly exists: %v", err)
	}
	testutil.FailErr(t, "cleanup next locked startup", CleanupInterruptedTransfers(root))
	_, discovered, err := LatestUpgradeRecovery(t.Context(), root)
	testutil.FailErr(t, "discover embedded recovery descriptor", err)
	if discovered.Snapshot != record.Snapshot {
		t.Fatal("published recovery became undiscoverable")
	}
	testutil.FailErr(t, "retry interrupted upgrade", CaptureUpgradeRecovery(t.Context(), opts, plan, record.TargetAppVersion))
	pending, err := readUpgradeRecovery(filepath.Join(recoveryRoot, "pending.json"))
	testutil.FailErr(t, "read repaired pending pointer", err)
	if pending.Snapshot != record.Snapshot {
		t.Fatal("retry recopied the retained installation")
	}
	testutil.FailErr(t, "prepare current attempt writers", DetachUnclaimedUpgradeRecoveries(root, record.TargetAppVersion))
	if _, err := os.Stat(filepath.Join(recoveryRoot, record.Snapshot+".json")); !os.IsNotExist(err) {
		t.Fatalf("active pending attempt was detached: %v", err)
	}
	records, err := upgradeRecoveryRecords(recoveryRoot)
	testutil.FailErr(t, "list deduplicated recovery descriptors", err)
	if len(records) != 1 {
		t.Fatalf("recovery descriptors=%d want1", len(records))
	}
	testutil.FailErr(t, "complete recovered upgrade", CompleteUpgradeRecovery(root, record.TargetAppVersion))
	_, completed, err := LatestUpgradeRecovery(t.Context(), root)
	testutil.FailErr(t, "read authoritative completion metadata", err)
	if completed.ReadyAt == "" {
		t.Fatal("embedded descriptor shadowed completed readiness")
	}
}

func TestRecoveryPublicationCorruptEmbeddedSnapshotRefusesRetry(t *testing.T) {
	root := t.TempDir()
	opts, plan, record := publishFixtureRecoveryWithoutPointer(t, root)
	manifest := filepath.Join(root, db.UpgradeRecoveryDirName, record.Snapshot, "manifest.json")
	testutil.FailErr(t, "corrupt completed capture", os.WriteFile(manifest, []byte("corrupt"), 0o600))
	if err := CaptureUpgradeRecovery(t.Context(), opts, plan, record.TargetAppVersion); err == nil {
		t.Fatal("corrupt matching recovery silently replaced")
	}
	if _, err := os.Stat(filepath.Join(root, db.UpgradeRecoveryDirName, "pending.json")); !os.IsNotExist(err) {
		t.Fatalf("corrupt recovery published pointer: %v", err)
	}
	testutil.FailErr(t, "cleanup preserves failed published capture", CleanupInterruptedTransfers(root))
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("failed published capture deleted: %v", err)
	}
}

func TestRecoveryPublishedSnapshotDetachesWhenAnotherVersionBecomesReady(t *testing.T) {
	root := t.TempDir()
	opts, plan, record := publishFixtureRecoveryWithoutPointer(t, root)
	testutil.FailErr(t, "detach before older version starts writers", DetachUnclaimedUpgradeRecoveries(root, opts.AppVersion))
	detached, err := readUpgradeRecovery(filepath.Join(root, db.UpgradeRecoveryDirName, record.Snapshot+".json"))
	testutil.FailErr(t, "read preserved failed target", err)
	if detached.ReadyAt != "" {
		t.Fatal("older application claimed newer target readiness")
	}
	testutil.FailErr(t, "capture after older application continued", CaptureUpgradeRecovery(t.Context(), opts, plan, record.TargetAppVersion))
	pending, err := readUpgradeRecovery(filepath.Join(root, db.UpgradeRecoveryDirName, "pending.json"))
	testutil.FailErr(t, "read fresh attempt pointer", err)
	if pending.Snapshot == record.Snapshot {
		t.Fatal("continued older app reused stale capture")
	}
	if _, err := os.Stat(filepath.Join(root, db.UpgradeRecoveryDirName, record.Snapshot)); err != nil {
		t.Fatalf("prior failed point removed: %v", err)
	}
}

func TestRecoveryPendingSnapshotDetachesBeforeOlderVersionWriters(t *testing.T) {
	root := t.TempDir()
	opts, plan, record := publishFixtureRecoveryWithoutPointer(t, root)
	recoveryRoot := filepath.Join(root, db.UpgradeRecoveryDirName)
	testutil.FailErr(t, "publish original pending attempt", publishUpgradePending(recoveryRoot, record))
	testutil.FailErr(t, "detach before older version writers", DetachUnclaimedUpgradeRecoveries(root, opts.AppVersion))
	if _, err := os.Stat(filepath.Join(recoveryRoot, "pending.json")); !os.IsNotExist(err) {
		t.Fatalf("mismatched target remains reusable: %v", err)
	}
	detached, err := readUpgradeRecovery(filepath.Join(recoveryRoot, record.Snapshot+".json"))
	testutil.FailErr(t, "read detached pending attempt", err)
	if detached.ReadyAt != "" {
		t.Fatal("older version stamped target successful")
	}
	testutil.FailErr(t, "capture after older app writes", CaptureUpgradeRecovery(t.Context(), opts, plan, record.TargetAppVersion))
	pending, err := readUpgradeRecovery(filepath.Join(recoveryRoot, "pending.json"))
	testutil.FailErr(t, "read fresh capture", err)
	if pending.Snapshot == record.Snapshot {
		t.Fatal("reused point from before intervening older app")
	}
}

func TestRecoveryIncompleteCaptureIsReclaimedWithoutDeletingUUIDDirectories(t *testing.T) {
	root := t.TempDir()
	recoveryRoot := filepath.Join(root, db.UpgradeRecoveryDirName)
	temporary := filepath.Join(recoveryRoot, ".backup-snapshot-"+uuid.NewString())
	unknown := filepath.Join(recoveryRoot, uuid.NewString())
	for _, path := range []string{temporary, unknown} {
		testutil.FailErr(t, "create interrupted capture directory", os.MkdirAll(path, 0o700))
		testutil.FailErr(t, "write partial captured body", os.WriteFile(filepath.Join(path, "partial"), []byte("retained bytes"), 0o600))
	}
	testutil.FailErr(t, "cleanup interrupted capture", CleanupInterruptedTransfers(root))
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("partial capture remains: %v", err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("unrecognized UUID directory was deleted: %v", err)
	}
}

func publishFixtureRecoveryWithoutPointer(t *testing.T, root string) (CreateOpts, migrations.Plan, UpgradeRecovery) {
	t.Helper()
	path := filepath.Join(root, storeRelPath)
	database := testdbfixture.OpenPath(t, path)
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan captured baseline", err)
	opts := CreateOpts{ConfigDir: root, DBPath: path, SQLDB: database, SchemaUserVersion: db.SchemaVersion, AppVersion: "1.0.0"}
	recoveryRoot := filepath.Join(root, db.UpgradeRecoveryDirName)
	testutil.FailErr(t, "create recovery namespace", os.MkdirAll(recoveryRoot, 0o700))
	name := uuid.NewString()
	temporary := ".backup-snapshot-" + name
	manifest, err := captureRecoveryDirectory(t.Context(), opts, filepath.Join(recoveryRoot, temporary))
	testutil.FailErr(t, "capture complete temporary snapshot", err)
	inventory, err := manifestInventory(manifest)
	testutil.FailErr(t, "bind captured inventory", err)
	record := UpgradeRecovery{Snapshot: name, Inventory: inventory, Source: plan.Source, Target: plan.Target, SourceAppVersion: opts.AppVersion, TargetAppVersion: "1.0.1", CreatedAt: manifest.CreatedAt}
	testutil.FailErr(t, "atomically publish payload with embedded descriptor", publishRecoveryDirectory(recoveryRoot, temporary, record))
	return opts, plan, record
}
