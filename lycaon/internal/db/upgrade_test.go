package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

const upgradeTestSchema = `
CREATE TABLE store_meta(key TEXT PRIMARY KEY, value TEXT);
INSERT INTO store_meta VALUES('clean_shutdown','1');
CREATE TABLE history(id TEXT PRIMARY KEY, body TEXT NOT NULL);
INSERT INTO history VALUES('retained','the original text');
CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, checksum TEXT NOT NULL, from_revision INTEGER NOT NULL, to_revision INTEGER UNIQUE NOT NULL, applied_at TEXT NOT NULL);
PRAGMA user_version=1;`

func TestCurrentBaselineRequiresNoUpgrade(t *testing.T) {
	baseline, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect current baseline", err)
	t.Logf("current schema baseline: revision=%d shape=%s", baseline.Revision, baseline.Shape)
	plan, err := PlanSchemaUpgrade(t.Context(), baseline)
	testutil.FailErr(t, "plan current baseline", err)
	if plan.Required() || plan.Source != baseline || plan.Target != baseline {
		t.Fatalf("current baseline plan = %+v, want unchanged %+v", plan, baseline)
	}
}

func upgradeFixture(t *testing.T) (string, *migrations.Registry, migrations.Plan) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	w, err := openWriter(t.Context(), path, StoreOptions{})
	testutil.FailErr(t, "open upgrade fixture", err)
	_, err = w.ExecContext(t.Context(), upgradeTestSchema)
	testutil.FailErr(t, "seed retained history", err)
	first, err := inspectSchema(t.Context(), w)
	testutil.FailErr(t, "read initial schema", err)
	statements := []string{
		`ALTER TABLE history ADD COLUMN retained INTEGER NOT NULL DEFAULT 1; PRAGMA user_version=2;`,
		`ALTER TABLE history ADD COLUMN label TEXT NOT NULL DEFAULT 'preserved'; PRAGMA user_version=3;`,
	}
	baselines := []migrations.Baseline{first}
	steps := []migrations.Step{}
	for i, statement := range statements {
		_, err = w.ExecContext(t.Context(), statement)
		testutil.FailErr(t, "derive next schema", err)
		next, inspectErr := inspectSchema(t.Context(), w)
		testutil.FailErr(t, "inspect next schema", inspectErr)
		sum := sha256.Sum256([]byte(statement))
		steps = append(steps, migrations.Step{ID: "step-" + string(rune('a'+i)), Checksum: hex.EncodeToString(sum[:]), Source: []byte(statement), From: baselines[i], To: next,
			Apply: func(ctx context.Context, tx *sql.Tx) error {
				_, applyErr := tx.ExecContext(ctx, statement)
				return applyErr
			},
		})
		baselines = append(baselines, next)
	}
	testutil.FailErr(t, "close schema derivation", w.Close())
	testutil.FailErr(t, "remove fixture derivation", os.Remove(path))
	w, err = openWriter(t.Context(), path, StoreOptions{})
	testutil.FailErr(t, "reopen source fixture", err)
	_, err = w.ExecContext(t.Context(), upgradeTestSchema)
	testutil.FailErr(t, "seed upgrade source", err)
	testutil.FailErr(t, "close source fixture", w.Close())
	registry, err := migrations.New(baselines[2], baselines, steps)
	testutil.FailErr(t, "register skipped-release route", err)
	plan, err := registry.Plan(first)
	testutil.FailErr(t, "plan skipped-release route", err)
	return path, registry, plan
}

func TestUpgradeRequiresRecoveryBeforeAnyMutation(t *testing.T) {
	for _, captureFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "failed"}[captureFails], func(t *testing.T) {
			path, registry, _ := upgradeFixture(t)
			before, err := os.ReadFile(path)
			testutil.FailErr(t, "read initial bytes", err)
			hooks := UpgradeHooks{}
			if captureFails {
				hooks.Before = func(context.Context, UpgradeSource, migrations.Plan) error { return errors.New("disk full") }
			}
			if err := upgradeExistingUsing(t.Context(), path, hooks, false, registry); !errors.Is(err, ErrStoreIncompatible) {
				t.Fatalf("upgrade error = %v, want recoverable refusal", err)
			}
			after, err := os.ReadFile(path)
			testutil.FailErr(t, "read refused bytes", err)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refused upgrade changed source bytes")
			}
		})
	}
}

func TestUpgradeRefusesConfirmedDamageBeforeCapture(t *testing.T) {
	path, registry, _ := upgradeFixture(t)
	const diagnosis = "foreign_key_check failed: project_roots references projects"
	w, err := openWriter(t.Context(), path, StoreOptions{})
	testutil.FailErr(t, "open marker fixture", err)
	_, err = w.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES(?,?)`, integrityAuditFailedMetaKey, diagnosis)
	testutil.FailErr(t, "seed confirmed damage", err)
	testutil.FailErr(t, "close marker fixture", w.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read initial bytes", err)
	hooks := UpgradeHooks{Before: func(context.Context, UpgradeSource, migrations.Plan) error {
		t.Fatal("recovery capture ran on a store with confirmed damage")
		return nil
	}}
	err = upgradeExistingUsing(t.Context(), path, hooks, false, registry)
	var incompatible *StoreIncompatibleError
	if !errors.As(err, &incompatible) || !strings.Contains(incompatible.Detail, diagnosis) {
		t.Fatalf("upgrade error = %v, want refusal with the recorded diagnosis", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused bytes", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refused upgrade changed source bytes")
	}
}

func TestUpgradeSkippedReleasesPreservesHistoryAndSnapshot(t *testing.T) {
	path, registry, plan := upgradeFixture(t)
	snapshot := path + ".before"
	var phases []migrations.Phase
	committed := false
	hooks := UpgradeHooks{
		Before: func(ctx context.Context, source UpgradeSource, got migrations.Plan) error {
			if got.Source != plan.Source || got.Target != plan.Target {
				t.Fatal("capture received another route")
			}
			return CreateSnapshot(ctx, source.DB, snapshot)
		},
		After:    func(ctx context.Context, got migrations.Plan) error { committed = true; return nil },
		Progress: func(phase migrations.Phase) { phases = append(phases, phase) },
	}
	testutil.FailErr(t, "upgrade skipped releases", upgradeExistingUsing(t.Context(), path, hooks, false, registry))
	if !committed || len(phases) < 4 || phases[0] != migrations.Inspecting || phases[1] != migrations.Snapshotting {
		t.Fatalf("upgrade lifecycle incomplete: committed=%v phases=%v", committed, phases)
	}
	reader, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open upgraded history", err)
	defer func() { _ = reader.Close() }()
	var body, label string
	var retained, applied int
	testutil.FailErr(t, "read preserved history", reader.QueryRow(`SELECT body, label, retained FROM history WHERE id='retained'`).Scan(&body, &label, &retained))
	if body != "the original text" || label != "preserved" || retained != 1 {
		t.Fatalf("history changed: %q %q %d", body, label, retained)
	}
	testutil.FailErr(t, "read migration ledger", reader.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&applied))
	if applied != 2 {
		t.Fatalf("applied steps = %d", applied)
	}
	previous, err := openReader(t.Context(), snapshot)
	testutil.FailErr(t, "open retained snapshot", err)
	defer func() { _ = previous.Close() }()
	identity, err := inspectSchema(t.Context(), previous)
	testutil.FailErr(t, "inspect snapshot schema", err)
	if identity != plan.Source {
		t.Fatalf("snapshot = %v, want %v", identity, plan.Source)
	}
	committed = false
	testutil.FailErr(t, "restart upgraded installation", upgradeExistingUsing(t.Context(), path, hooks, false, registry))
	if committed {
		t.Fatal("restart repeated committed migration")
	}
}

func TestUpgradeCancellationRollsBackWholeChain(t *testing.T) {
	path, registry, plan := upgradeFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	steps := 0
	hooks := UpgradeHooks{Progress: func(phase migrations.Phase) {
		if phase == migrations.Migrating {
			steps++
			if steps == 2 {
				cancel()
			}
		}
	}}
	if err := upgradeExistingUsing(ctx, path, hooks, true, registry); err == nil {
		t.Fatal("canceled upgrade succeeded")
	}
	reader, err := openReader(t.Context(), path)
	testutil.FailErr(t, "reopen interrupted upgrade", err)
	defer func() { _ = reader.Close() }()
	identity, err := inspectSchema(t.Context(), reader)
	testutil.FailErr(t, "inspect rollback", err)
	if identity != plan.Source {
		t.Fatalf("partial schema escaped transaction: %v", identity)
	}
	var count int
	testutil.FailErr(t, "read rolled-back ledger", reader.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count))
	if count != 0 {
		t.Fatalf("partial ledger escaped rollback: %d", count)
	}
	testutil.FailErr(t, "resume interrupted staged upgrade", upgradeExistingUsing(t.Context(), path, UpgradeHooks{}, true, registry))
}

func TestUpgradeRefusesAlteredAppliedMigration(t *testing.T) {
	path, registry, _ := upgradeFixture(t)
	testutil.FailErr(t, "upgrade staged fixture", upgradeExistingUsing(t.Context(), path, UpgradeHooks{}, true, registry))
	w, err := openWriter(t.Context(), path, StoreOptions{})
	testutil.FailErr(t, "open ledger fixture", err)
	_, err = w.Exec(`UPDATE schema_migrations SET checksum='changed'`)
	testutil.FailErr(t, "alter ledger identity", err)
	testutil.FailErr(t, "close altered ledger", w.Close())
	if err := upgradeExistingUsing(t.Context(), path, UpgradeHooks{}, true, registry); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("altered ledger error = %v", err)
	}
}

// A volume too full for the upgrade is its own refusal: the store is
// untouched and the numbers travel with it.
func TestUpgradeRefusesAFullVolumeWithoutTouchingTheStore(t *testing.T) {
	path, registry, _ := upgradeFixture(t)
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read initial bytes", err)
	space := &InsufficientSpaceError{NeededBytes: 3 << 30, AvailableBytes: 1 << 30}
	hooks := UpgradeHooks{Before: func(context.Context, UpgradeSource, migrations.Plan) error {
		return fmt.Errorf("capture: %w", space)
	}}
	err = upgradeExistingUsing(t.Context(), path, hooks, false, registry)
	var refused *StoreIncompatibleError
	if !errors.As(err, &refused) || refused.Reason != RecoveryReasonInsufficientSpace || refused.Space != space {
		t.Fatalf("upgrade error = %v, want an insufficient-space refusal carrying its numbers", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused bytes", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refused upgrade changed source bytes")
	}
}

func TestUpgradeOperationalFailurePreservesSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		cause  error
		reason RecoveryReason
	}{
		{"permission", os.ErrPermission, RecoveryReasonStorageUnavailable},
		{"io", syscall.EIO, RecoveryReasonStorageUnavailable},
		{"full", syscall.ENOSPC, RecoveryReasonInsufficientSpace},
		{"quota", syscall.EDQUOT, RecoveryReasonInsufficientSpace},
		{"canceled", context.Canceled, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, registry, _ := upgradeFixture(t)
			before, err := os.ReadFile(path)
			testutil.FailErr(t, "read source before failed capture", err)
			err = upgradeExistingUsing(t.Context(), path, UpgradeHooks{Before: func(context.Context, UpgradeSource, migrations.Plan) error {
				return fmt.Errorf("capture: %w", test.cause)
			}}, false, registry)
			var failure *StoreIncompatibleError
			if test.reason == "" {
				if !errors.Is(err, test.cause) || errors.As(err, &failure) {
					t.Fatalf("cancellation = %v, want cancellation without recovery diagnosis", err)
				}
			} else if !errors.As(err, &failure) || failure.Reason != test.reason {
				t.Fatalf("capture failure = %v, want %s", err, test.reason)
			}
			after, err := os.ReadFile(path)
			testutil.FailErr(t, "read source after failed capture", err)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed recovery capture changed source bytes")
			}
		})
	}
}

func TestUpgradeRefusesRecordedDamageButAllowsPendingAudit(t *testing.T) {
	for _, value := range []string{"confirmed damage", "integrity audit pending"} {
		t.Run(value, func(t *testing.T) {
			path, registry, plan := upgradeFixture(t)
			writer, err := openWriter(t.Context(), path, StoreOptions{})
			testutil.FailErr(t, "open audit fixture", err)
			_, err = writer.Exec(`INSERT INTO store_meta(key, value) VALUES(?,?)`, integrityAuditFailedMetaKey, value)
			testutil.FailErr(t, "record audit state", err)
			testutil.FailErr(t, "close audit fixture", writer.Close())
			err = upgradeExistingUsing(t.Context(), path, UpgradeHooks{}, true, registry)
			if value == "integrity audit pending" {
				testutil.FailErr(t, "upgrade pending audit", err)
				return
			}
			var failure *StoreIncompatibleError
			if !errors.As(err, &failure) || failure.Reason != RecoveryReasonIntegrityFailed {
				t.Fatalf("recorded damage error = %v", err)
			}
			reader, err := openReader(t.Context(), path)
			testutil.FailErr(t, "read refused source", err)
			defer func() { _ = reader.Close() }()
			actual, err := inspectSchema(t.Context(), reader)
			testutil.FailErr(t, "inspect refused source", err)
			if actual != plan.Source {
				t.Fatal("recorded damage was migrated")
			}
		})
	}
}

func TestUpgradeClassifiesSQLiteDamageDuringMigration(t *testing.T) {
	path, _, plan := upgradeFixture(t)
	damagedPath := filepath.Join(t.TempDir(), "damaged.db")
	testutil.FailErr(t, "seed invalid SQLite header", os.WriteFile(damagedPath, []byte("this is not a SQLite database and cannot be read"), 0600))
	source := []byte("attach damaged database")
	sum := sha256.Sum256(source)
	target := migrations.Baseline{Revision: plan.Source.Revision + 1, Shape: plan.Source.Shape}
	registry, err := migrations.New(target, []migrations.Baseline{plan.Source}, []migrations.Step{{
		ID: "damaged-source", Source: source, Checksum: hex.EncodeToString(sum[:]), From: plan.Source, To: target,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `ATTACH DATABASE ? AS damaged`, damagedPath)
			return err
		},
	}})
	testutil.FailErr(t, "register failing migration", err)
	err = upgradeExistingUsing(t.Context(), path, UpgradeHooks{}, true, registry)
	var failure *StoreIncompatibleError
	if !errors.As(err, &failure) || failure.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("SQLite damage was not classified as integrity failure: %v", err)
	}
	reader, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open rolled back source", err)
	defer func() { _ = reader.Close() }()
	actual, err := inspectSchema(t.Context(), reader)
	testutil.FailErr(t, "inspect rolled back source", err)
	if actual != plan.Source {
		t.Fatal("failed migration changed source schema")
	}
}
