package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleasedRewindUpgradePreservesIdentityWithoutInstallationPaths(t *testing.T) {
	for _, version := range []string{"1.0.0", "1.0.1"} {
		t.Run(version, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", version, "store.db"))
			testutil.FailErr(t, "read released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0600))
			source, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "open released store", err)
			for _, phase := range []string{"prepared", "applying", "files_applied", "committed", "rolled_back", "diverged"} {
				_, err = source.Exec(`INSERT INTO rewind_operations(id,session_id,anchor_message_id,input_digest,project_dir,journal_path,status,error,created_at,updated_at) SELECT ?,id,'anchor','digest','/project','/original-install/journal.json',?,'','2026-01-01','2026-01-01' FROM sessions LIMIT 1`, phase, phase)
				testutil.FailErr(t, "seed released receipt", err)
			}
			columns := "id,session_id,anchor_message_id,input_digest,project_dir,status,error,created_at,updated_at"
			rows, err := source.Query("SELECT " + columns + " FROM rewind_operations")
			testutil.FailErr(t, "read released receipts", err)
			before := retainedRows(t, rows)
			testutil.FailErr(t, "close released store", source.Close())
			for boot := 0; boot < 2; boot++ {
				testutil.FailErr(t, "upgrade released rewind receipts", UpgradeStaged(t.Context(), path))
				reader, err := openReader(t.Context(), path)
				testutil.FailErr(t, "open upgraded store", err)
				rows, err := reader.Query("SELECT " + columns + " FROM rewind_operations")
				testutil.FailErr(t, "read upgraded receipts", err)
				after := retainedRows(t, rows)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("upgrade changed operation identity or phase")
				}
				var obsolete int
				testutil.FailErr(t, "inspect receipt columns", reader.QueryRow(`SELECT count(*) FROM pragma_table_info('rewind_operations') WHERE name='journal_path'`).Scan(&obsolete))
				if obsolete != 0 {
					t.Fatal("receipt still stores installation path")
				}
				testutil.FailErr(t, "close upgraded store", reader.Close())
			}
		})
	}
}
