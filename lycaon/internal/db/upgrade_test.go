package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
				hooks.Before = func(context.Context, *sql.DB, migrations.Plan) error { return errors.New("disk full") }
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

func TestUpgradeSkippedReleasesPreservesHistoryAndSnapshot(t *testing.T) {
	path, registry, plan := upgradeFixture(t)
	snapshot := path + ".before"
	var phases []migrations.Phase
	committed := false
	hooks := UpgradeHooks{
		Before: func(ctx context.Context, source *sql.DB, got migrations.Plan) error {
			if got.Source != plan.Source || got.Target != plan.Target {
				t.Fatal("capture received another route")
			}
			return CreateSnapshot(ctx, source, snapshot)
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

func TestReleasedProvenanceUpgradeRequiresRecoveryAndPreservesHistory(t *testing.T) {
	source := filepath.Join(testutil.CheckoutRoot(t), "lycaon", "testdata", "upgrade-corpus", "1.0.1", "store.db")
	original, err := os.ReadFile(source)
	testutil.FailErr(t, "read released store", err)
	path := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy released store", os.WriteFile(path, original, 0600))
	if err := upgradeExisting(t.Context(), path, UpgradeHooks{}, false); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("live upgrade without capture: %v", err)
	}
	untouched, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused store", err)
	if !reflect.DeepEqual(original, untouched) {
		t.Fatal("refused upgrade changed released bytes")
	}
	snapshot := path + ".before"
	hooks := UpgradeHooks{Before: func(ctx context.Context, source *sql.DB, plan migrations.Plan) error {
		if plan.Source.Revision != 1 || plan.Target.Revision != 2 {
			t.Fatalf("wrong migration route: %+v", plan)
		}
		return CreateSnapshot(ctx, source, snapshot)
	}}
	testutil.FailErr(t, "upgrade released store with recovery", upgradeExisting(t.Context(), path, hooks, false))
	upgraded, err := openReader(t.Context(), path)
	testutil.FailErr(t, "read upgraded store", err)
	defer upgraded.Close()
	previous, err := openReader(t.Context(), snapshot)
	testutil.FailErr(t, "read recovery snapshot", err)
	defer previous.Close()
	for _, table := range []string{"workflow_runs", "messages", "worker_jobs"} {
		var before, after int
		testutil.FailErr(t, "count released history", previous.QueryRow("SELECT count(*) FROM "+table).Scan(&before))
		testutil.FailErr(t, "count upgraded history", upgraded.QueryRow("SELECT count(*) FROM "+table).Scan(&after))
		if before != after {
			t.Fatalf("%s history changed: %d != %d", table, before, after)
		}
	}
	testutil.FailErr(t, "verify upgraded baseline", CheckBaseline(t.Context(), upgraded))
	testutil.FailErr(t, "restart upgraded store", upgradeExisting(t.Context(), path, UpgradeHooks{}, false))
}
