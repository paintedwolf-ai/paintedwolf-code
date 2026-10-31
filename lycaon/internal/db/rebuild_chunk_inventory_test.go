package db

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedChunkInventoryUsesCappedMainWithoutTemporaryBTrees(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 3, "/work/b": 1})
	reader, err := openReader(t.Context(), source)
	testutil.FailErr(t, "open source", err)
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan source", err)
	testutil.FailErr(t, "close source", reader.Close())
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open private fixture", err)
	defer database.Close()
	_, err = database.ExecContext(t.Context(), "ATTACH DATABASE ? AS original", fileDSN(source, "mode=ro"))
	testutil.FailErr(t, "attach source", err)
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin inventory", err)
	defer tx.Rollback()
	selection, err := prepareRebuildChunkInventory(t.Context(), tx, plan.CopyPlan())
	testutil.FailErr(t, "prepare inventory", err)
	testutil.FailErr(t, "verify source reference coverage", validateRebuildChunkInventory(t.Context(), tx))
	for _, table := range []string{"source_manifest_entries", "source_manifest_chunks"} {
		for _, query := range []string{"SELECT e.* " + selection.Sources[table], selection.ExpectedRows[table]} {
			rows, err := tx.QueryContext(t.Context(), "EXPLAIN "+query)
			testutil.FailErr(t, "inspect actual SQLite allocation plan", err)
			for rows.Next() {
				var address, p1, p2, p3, p5 int
				var opcode string
				var p4, comment any
				testutil.FailErr(t, "read VDBE instruction", rows.Scan(&address, &opcode, &p1, &p2, &p3, &p4, &p5, &comment))
				if opcode == "OpenEphemeral" || opcode == "SorterOpen" {
					t.Fatalf("%s allocated uncapped temporary B-tree via %s", table, opcode)
				}
			}
			testutil.FailErr(t, "finish VDBE plan", rows.Err())
			//nolint:sqlclosecheck // Closed after each EXPLAIN query in the loop.
			testutil.FailErr(t, "close VDBE plan", rows.Close())
		}
	}
	_, err = tx.ExecContext(t.Context(), "DELETE FROM _upgrade_retained_chunks WHERE id=(SELECT chunk_id FROM original.source_snapshot_chunks NOT INDEXED LIMIT 1)")
	testutil.FailErr(t, "inject lost reference", err)
	if err := validateRebuildChunkInventory(t.Context(), tx); !errors.Is(err, migrations.ErrIntegrity) {
		t.Fatalf("missing source chunk was admitted: %v", err)
	}
}
