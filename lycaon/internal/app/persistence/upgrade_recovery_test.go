package persistence

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
)

func TestUnwritableRecoveryMetadataKeepsRecoveryRoutesEligible(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "store.db")
	database, err := db.Open(dbPath)
	testutil.FailErr(t, "open source", err)
	testutil.FailErr(t, "stamp current application", db.New(database).UpsertStoreMeta(t.Context(), db.UpsertStoreMetaParams{Key: "app_version", Value: version.Version}))
	testutil.FailErr(t, "close source", database.Close())
	testutil.FailErr(t, "block recovery metadata with directory", os.MkdirAll(filepath.Join(root, db.UpgradeRecoveryDirName, "pending.json"), 0o700))
	builder := &Runtime{logger: slog.Default()}
	database, err = builder.openUpgradeableStore(t.Context(), dbPath)
	if database != nil {
		_ = database.Close()
		t.Fatal("store exposed despite recovery metadata failure")
	}
	var unavailable *db.StoreIncompatibleError
	if !errors.As(err, &unavailable) || unavailable.Reason != db.RecoveryReasonStorageUnavailable {
		t.Fatalf("startup error=%v; want unavailable storage without a corruption diagnosis", err)
	}
}

func TestRecoverySnapshotPrecedesRetention(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	dbPath := filepath.Join(root, "store.db")
	database, err := db.Open(dbPath)
	testutil.FailErr(t, "open source", err)
	_, err = database.ExecContext(ctx, `INSERT INTO command_invocations(operation_id,input_digest,response_json,created_at) VALUES ('op1','digest','{}','2000-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed retention victim", err)
	testutil.FailErr(t, "stamp older application", db.New(database).UpsertStoreMeta(ctx, db.UpsertStoreMetaParams{Key: "app_version", Value: "0.0.1"}))
	testutil.FailErr(t, "create durable drafts", os.MkdirAll(filepath.Join(root, "drafts"), 0o700))
	bodyPath := filepath.Join(root, "drafts", "retained.txt")
	testutil.FailErr(t, "write durable body", os.WriteFile(bodyPath, []byte("preserved body"), 0o600))
	testutil.FailErr(t, "close source", database.Close())
	builder := &Runtime{logger: slog.Default()}
	database, err = builder.openUpgradeableStore(t.Context(), dbPath)
	testutil.FailErr(t, "open application update", err)
	defer database.Close()
	_, err = db.RunRetention(ctx, database, db.RetentionConfig{Enabled: true, OperationJournals: time.Nanosecond})
	testutil.FailErr(t, "run retention", err)
	testutil.FailErr(t, "remove live body", os.Remove(bodyPath))
	var live int
	testutil.FailErr(t, "count live receipt", database.QueryRowContext(ctx, `SELECT count(*) FROM command_invocations WHERE operation_id='op1'`).Scan(&live))
	if live != 0 {
		t.Fatalf("live count=%d want 0", live)
	}
	_, _, err = backup.LatestUpgradeRecovery(ctx, root)
	testutil.FailErr(t, "find complete recovery", err)
	restored := t.TempDir()
	testutil.FailErr(t, "relocate complete recovery", os.CopyFS(filepath.Join(restored, db.UpgradeRecoveryDirName), os.DirFS(filepath.Join(root, db.UpgradeRecoveryDirName))))
	_, err = backup.StageLatestUpgradeRecovery(ctx, backup.StageOpts{ConfigDir: restored, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage self-contained recovery", err)
	testutil.FailErr(t, "apply recovery", backup.ApplyPending(t.Context(), restored))
	recovered, err := db.OpenReadOnly(ctx, filepath.Join(restored, "store.db"))
	testutil.FailErr(t, "open recovered database", err)
	defer recovered.Close()
	var retained int
	testutil.FailErr(t, "count recovered receipt", recovered.QueryRowContext(ctx, `SELECT count(*) FROM command_invocations WHERE operation_id='op1'`).Scan(&retained))
	if retained != 1 {
		t.Fatalf("recovery count=%d want 1", retained)
	}
	body, err := os.ReadFile(filepath.Join(restored, "drafts", "retained.txt"))
	testutil.FailErr(t, "read recovered body", err)
	if string(body) != "preserved body" {
		t.Fatalf("recovered body=%q", body)
	}
}

func TestStoreOpenDetachesEmbeddedRecoveryBeforeWritersStart(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	dbPath := filepath.Join(root, "store.db")
	database, err := db.Open(dbPath)
	testutil.FailErr(t, "open source", err)
	testutil.FailErr(t, "stamp older application", db.New(database).UpsertStoreMeta(ctx, db.UpsertStoreMetaParams{Key: "app_version", Value: "0.0.1"}))
	testutil.FailErr(t, "close source", database.Close())
	builder := &Runtime{logger: slog.Default()}
	database, err = builder.openUpgradeableStore(t.Context(), dbPath)
	testutil.FailErr(t, "capture application update", err)
	testutil.FailErr(t, "stamp current application", db.New(database).UpsertStoreMeta(ctx, db.UpsertStoreMetaParams{Key: "app_version", Value: version.Version}))
	testutil.FailErr(t, "close updated store", database.Close())
	testutil.FailErr(t, "simulate missing pending publication", os.Remove(filepath.Join(root, db.UpgradeRecoveryDirName, "pending.json")))
	restarted := &Runtime{logger: slog.Default()}
	database, err = restarted.openUpgradeableStore(t.Context(), dbPath)
	testutil.FailErr(t, "restart current application", err)
	defer database.Close()
	if restarted.UpgradeReady == nil {
		t.Fatal("successful startup must finalize embedded-only recovery points")
	}
	_, record, err := backup.LatestUpgradeRecovery(ctx, root)
	testutil.FailErr(t, "find preserved recovery point", err)
	if record.ReadyAt != "" {
		t.Fatal("embedded-only recovery point must remain unsuccessful")
	}
	_, err = os.Stat(filepath.Join(root, db.UpgradeRecoveryDirName, record.Snapshot+".json"))
	testutil.FailErr(t, "find detached recovery metadata before writers start", err)
	testutil.FailErr(t, "complete application readiness", restarted.UpgradeReady())
}

// An application update the volume cannot hold is refused for space, not as
// a failed integrity check.
func TestApplicationUpdateRefusesAFullVolumeForSpace(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "store.db")
	database, err := db.Open(dbPath)
	testutil.FailErr(t, "open source", err)
	testutil.FailErr(t, "stamp older application", db.New(database).UpsertStoreMeta(t.Context(), db.UpsertStoreMetaParams{Key: "app_version", Value: "0.0.1"}))
	testutil.FailErr(t, "close source", database.Close())
	space := &db.InsufficientSpaceError{NeededBytes: 2 << 30, AvailableBytes: 1 << 30}
	hooks := db.UpgradeHooks{
		Before:   func(context.Context, db.UpgradeSource, migrations.Plan) error { return space },
		Progress: func(migrations.Phase) {},
	}
	builder := &Runtime{logger: slog.Default()}
	source, err := db.OpenReadOnly(t.Context(), dbPath)
	testutil.FailErr(t, "inspect application update source", err)
	defer func() { _ = source.Close() }()
	plan, err := db.PlanUpgrade(t.Context(), source)
	testutil.FailErr(t, "plan application update", err)
	err = builder.captureVersionRecovery(t.Context(), source, plan, hooks)
	var refused *db.StoreIncompatibleError
	if !errors.As(err, &refused) || refused.Reason != db.RecoveryReasonInsufficientSpace || refused.Space != space {
		t.Fatalf("capture error = %v, want an insufficient-space refusal", err)
	}
	hooks.Before = func(context.Context, db.UpgradeSource, migrations.Plan) error { return os.ErrPermission }
	err = builder.captureVersionRecovery(t.Context(), source, plan, hooks)
	if !errors.As(err, &refused) || refused.Reason != db.RecoveryReasonStorageUnavailable {
		t.Fatalf("permission error = %v, want unavailable storage", err)
	}
	hooks.Before = func(context.Context, db.UpgradeSource, migrations.Plan) error { return context.Canceled }
	if err = builder.captureVersionRecovery(t.Context(), source, plan, hooks); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation = %v, want cancellation", err)
	}
}

func TestStoreQuarantinePrecedesVersionRecoveryCapture(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	database, err := db.Open(path)
	testutil.FailErr(t, "open source", err)
	testutil.FailErr(t, "stamp previous application", db.New(database).UpsertStoreMeta(t.Context(), db.UpsertStoreMetaParams{Key: "app_version", Value: "0.0.1"}))
	testutil.FailErr(t, "close source", database.Close())
	testutil.FailErr(t, "seed unsupported quarantine", os.WriteFile(path+db.IntegrityFailureSuffix, []byte(`{"version":2}`), 0o600))
	builder := &Runtime{logger: slog.Default()}
	database, err = builder.openUpgradeableStore(t.Context(), path)
	if database != nil {
		_ = database.Close()
		t.Fatal("quarantined store opened")
	}
	if !errors.Is(err, db.ErrStoreIncompatible) {
		t.Fatalf("open=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, db.UpgradeRecoveryDirName)); !os.IsNotExist(err) {
		t.Fatalf("quarantine attempted recovery capture: %v", err)
	}
}
