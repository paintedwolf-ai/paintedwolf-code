package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	_ "modernc.org/sqlite"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryRejectsAmbiguousOrIncompleteRoutes(t *testing.T) {
	first := Baseline{Revision: 1, Shape: strings.Repeat("a", 64)}
	second := Baseline{Revision: 2, Shape: strings.Repeat("b", 64)}
	source := []byte("SELECT 1")
	sum := sha256.Sum256(source)
	step := Step{Source: source, ID: "history-label", Checksum: hex.EncodeToString(sum[:]), From: first, To: second, Apply: func(context.Context, *sql.Tx) error { return nil }}
	for name, steps := range map[string][]Step{
		"mutated definition": {{ID: step.ID, Checksum: step.Checksum, Source: []byte("SELECT 2"), From: first, To: second, Apply: step.Apply}},
		"missing definition": {{ID: step.ID, Checksum: step.Checksum, From: first, To: second, Apply: step.Apply}},
		"missing step":       nil,
		"duplicate source":   {step, step},
		"reverse step":       {{ID: step.ID, Checksum: step.Checksum, Source: source, From: second, To: first, Apply: step.Apply}},
		"invalid checksum":   {{ID: step.ID, Checksum: "changed", Source: source, From: first, To: second, Apply: step.Apply}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(second, []Baseline{first}, steps); err == nil {
				t.Fatal("invalid registry accepted")
			}
		})
	}
	registry, err := New(second, []Baseline{first}, []Step{step})
	testutil.FailErr(t, "register valid route", err)
	for _, unsupported := range []Baseline{
		{Revision: 1, Shape: second.Shape},
		{Revision: 3, Shape: first.Shape},
		{Revision: 0, Shape: first.Shape},
	} {
		if _, err := registry.Plan(unsupported); err == nil {
			t.Fatalf("unsupported schema accepted: %v", unsupported)
		}
	}
	if _, err := New(second, []Baseline{{Revision: 2, Shape: first.Shape}}, nil); err == nil {
		t.Fatal("released baseline redefinition accepted")
	}
}

func TestPlanBudgetsEverySkippedMigrationWithoutOverflow(t *testing.T) {
	first := Baseline{Revision: 1, Shape: strings.Repeat("a", 64)}
	second := Baseline{Revision: 2, Shape: strings.Repeat("b", 64)}
	third := Baseline{Revision: 3, Shape: strings.Repeat("c", 64)}
	apply := func(context.Context, *sql.Tx) error { return nil }
	source := []byte("SELECT 1")
	sum := sha256.Sum256(source)
	steps := []Step{
		{ID: "one", Checksum: hex.EncodeToString(sum[:]), Source: source, From: first, To: second, Apply: apply, ScratchBytes: 128},
		{ID: "two", Checksum: hex.EncodeToString(sum[:]), Source: source, From: second, To: third, Apply: apply, ScratchBytes: 256},
	}
	registry, err := New(third, []Baseline{first, second}, steps)
	testutil.FailErr(t, "register budgeted route", err)
	plan, err := registry.Plan(first)
	testutil.FailErr(t, "plan skipped route", err)
	if plan.ScratchBytes() != 384 {
		t.Fatalf("scratch=%d want384", plan.ScratchBytes())
	}
	plan, err = registry.Plan(third)
	testutil.FailErr(t, "plan current store", err)
	if plan.ScratchBytes() != 0 {
		t.Fatal("current store has migration scratch budget")
	}
	steps[1].ScratchBytes = ^uint64(0)
	if _, err := New(third, []Baseline{first, second}, steps); err == nil {
		t.Fatal("overflowed working-space budget accepted")
	}
}

// A step that bounds its writes counts that bound; one that does not counts
// the whole store.
func TestPlanWriteBytesCountsUndeclaredStepsAsTheWholeStore(t *testing.T) {
	first := Baseline{Revision: 1, Shape: strings.Repeat("a", 64)}
	second := Baseline{Revision: 2, Shape: strings.Repeat("b", 64)}
	third := Baseline{Revision: 3, Shape: strings.Repeat("c", 64)}
	apply := func(context.Context, *sql.Tx) error { return nil }
	source := []byte("SELECT 1")
	sum := sha256.Sum256(source)
	bounded := func(context.Context, DBTX, uint64) (uint64, error) { return 4096, nil }
	steps := []Step{
		{ID: "undeclared", Checksum: hex.EncodeToString(sum[:]), Source: source, From: first, To: second, Apply: apply},
		{ID: "bounded", Checksum: hex.EncodeToString(sum[:]), Source: source, From: second, To: third, Apply: apply, Writes: bounded},
	}
	registry, err := New(third, []Baseline{first, second}, steps)
	testutil.FailErr(t, "register route", err)
	plan, err := registry.Plan(first)
	testutil.FailErr(t, "plan route", err)
	writes, err := plan.WriteBytes(t.Context(), nil, 1<<20)
	testutil.FailErr(t, "estimate writes", err)
	if writes != 1<<20+4096 {
		t.Fatalf("writes = %d, want the whole store plus the declared bound", writes)
	}
	plan, err = registry.Plan(second)
	testutil.FailErr(t, "plan bounded step", err)
	if writes, err = plan.WriteBytes(t.Context(), nil, 1<<30); err != nil || writes != 4096 {
		t.Fatalf("bounded writes = %d, %v", writes, err)
	}
}

func TestExecuteChecksIntegrityBeforeCommit(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open migration fixture", err)
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`CREATE TABLE store_meta(key TEXT PRIMARY KEY,value TEXT);
 CREATE TABLE history(body TEXT CHECK(body != 'invalid'));
 INSERT INTO history VALUES('retained');
 CREATE TABLE schema_migrations(id TEXT, checksum TEXT, from_revision INTEGER, to_revision INTEGER, applied_at TEXT);
 PRAGMA user_version=1`)
	testutil.FailErr(t, "seed migration fixture", err)
	first := Baseline{Revision: 1, Shape: strings.Repeat("a", 64)}
	second := Baseline{Revision: 2, Shape: strings.Repeat("b", 64)}
	source := []byte("insert a constraint violation")
	sum := sha256.Sum256(source)
	step := Step{ID: "invalid-data", Source: source, Checksum: hex.EncodeToString(sum[:]), From: first, To: second,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON; INSERT INTO history VALUES('invalid'); PRAGMA ignore_check_constraints=OFF`)
			return err
		}}
	registry, err := New(second, []Baseline{first}, []Step{step})
	testutil.FailErr(t, "register migration fixture", err)
	plan, err := registry.Plan(first)
	testutil.FailErr(t, "plan migration fixture", err)
	inspect := func(ctx context.Context, db DBTX) (Baseline, error) {
		var version int
		err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version)
		if version == 1 {
			return first, err
		}
		return second, err
	}
	if err := registry.Execute(t.Context(), database, plan, inspect, nil); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("migration error = %v, want integrity refusal", err)
	}
	var count, version, ledger int
	testutil.FailErr(t, "read retained rows", database.QueryRow(`SELECT count(*) FROM history WHERE body='retained'`).Scan(&count))
	testutil.FailErr(t, "read rollback revision", database.QueryRow(`PRAGMA user_version`).Scan(&version))
	testutil.FailErr(t, "read rollback ledger", database.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&ledger))
	if count != 1 || version != 1 || ledger != 0 {
		t.Fatalf("rollback: retained=%d revision=%d ledger=%d", count, version, ledger)
	}
	testutil.FailErr(t, "read rolled-back insert", database.QueryRow(`SELECT count(*) FROM history WHERE body='invalid'`).Scan(&count))
	if count != 0 {
		t.Fatal("invalid migration data escaped rollback")
	}
	var pending int
	testutil.FailErr(t, "read rolled-back audit obligation", database.QueryRow(`SELECT count(*) FROM store_meta`).Scan(&pending))
	if pending != 0 {
		t.Fatal("failed migration committed audit metadata")
	}
}

func TestDropReleasedEnforcesRegisteredRetirements(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open retirement fixture", err)
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`CREATE TABLE legacy(kept TEXT, obsolete TEXT)`)
	testutil.FailErr(t, "seed retirement fixture", err)
	colRetirement := Retirement{Table: "legacy", Column: "obsolete", Capture: "SELECT obsolete FROM legacy", Rebuild: "SELECT kept FROM legacy"}
	tblRetirement := Retirement{Table: "legacy", Capture: "SELECT kept FROM legacy", Rebuild: "SELECT kept FROM legacy"}
	retirements["retire-step"] = []Retirement{colRetirement, tblRetirement}
	defer delete(retirements, "retire-step")

	registered := RegisteredRetirements()
	if len(registered["retire-step"]) != 2 {
		t.Fatalf("registered retirements = %v", registered)
	}

	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin retirement transaction", err)
	defer func() { _ = tx.Rollback() }()
	if err := DropReleased(t.Context(), tx, "retire-step", Retirement{Table: "unregistered"}); err == nil {
		t.Fatal("unregistered retirement accepted")
	}
	testutil.FailErr(t, "drop registered column", DropReleased(t.Context(), tx, "retire-step", colRetirement))
	testutil.FailErr(t, "drop registered table", DropReleased(t.Context(), tx, "retire-step", tblRetirement))
	testutil.FailErr(t, "commit retirement transaction", tx.Commit())
}
