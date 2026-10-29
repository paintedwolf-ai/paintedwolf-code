package db

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The source is exact main6 plus the established native authority step;
// its complete identity must match the declared revision7 prerequisite.
func authorityRevision7Store(t *testing.T, path string) *sql.DB {
	t.Helper()
	source := mainRevision6Store(t, path)
	tx, err := source.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin established authority step", err)
	step := migrations.EffectPermitIdentity(migrations.Baseline{})
	testutil.FailErr(t, "apply established authority step", step.Apply(t.Context(), tx))
	_, err = tx.ExecContext(t.Context(), `PRAGMA user_version=7`)
	testutil.FailErr(t, "stamp established authority revision", err)
	testutil.FailErr(t, "commit authority source", tx.Commit())
	actual, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect complete authority source identity", err)
	if actual != migrations.ProgramReceipts(migrations.Baseline{}).From {
		t.Fatalf("authority input differs from declared prerequisite: %+v", actual)
	}
	return source
}

func TestProgramReceiptsExtendDeclaredAuthoritySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority.db")
	source := authorityRevision7Store(t, path)
	before, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect complete authority source", err)
	testutil.FailErr(t, "close authority source before registered upgrade", source.Close())
	testutil.FailErr(t, "upgrade complete registered authority route", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "reopen complete registered authority route", UpgradeStaged(t.Context(), path))
	source, err = sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded authority target", err)
	defer source.Close()
	fresh, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fresh.db"))
	testutil.FailErr(t, "open independent fresh receipt target", err)
	defer fresh.Close()
	testutil.FailErr(t, "install fresh receipt schema", execFreshSchema(t.Context(), fresh))
	want, err := ReadShape(t.Context(), fresh)
	testutil.FailErr(t, "read complete fresh receipt shape", err)
	got, err := ReadShape(t.Context(), source)
	testutil.FailErr(t, "read complete upgraded receipt shape", err)
	if diff := want.Diff(got); len(diff) != 0 {
		t.Fatalf("receipt upgrade differs from complete fresh target: %v", diff)
	}
	after, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect compared receipt target", err)
	if before != migrations.ProgramReceipts(after).From {
		t.Fatalf("registered receipt source differs from declared authority: %+v", before)
	}
	t.Logf("verified complete authority-to-receipt shape: source=%+v target=%+v", before, after)
}

func TestProgramReceiptsColumnlessRevision8IsRefusedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := authorityRevision7Store(t, path)
	_, err := source.ExecContext(t.Context(), `PRAGMA user_version=8`)
	testutil.FailErr(t, "claim receipt revision without receipt tables", err)
	testutil.FailErr(t, "close unknown source", source.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read unknown source bytes", err)
	var incompatible *StoreIncompatibleError
	if err := UpgradeStaged(t.Context(), path); !errors.As(err, &incompatible) || incompatible.Reason != RecoveryReasonSchemaMismatch || incompatible.StoreSchemaVersion != 8 {
		t.Fatalf("unknown receipt shape was not refused: %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused source bytes", err)
	if !bytes.Equal(before, after) {
		t.Fatal("refused receipt store was mutated")
	}
}
