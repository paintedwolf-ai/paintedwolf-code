package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleasedRecoveryManifestsEnterBoundedReclamation(t *testing.T) {
	for _, version := range []string{"1.0.0", "1.0.1"} {
		t.Run(version, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", version, "store.db"))
			testutil.FailErr(t, "read released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0600))
			source, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "open released store", err)
			for _, id := range []string{"orphan", "retained"} {
				_, err = source.Exec(`INSERT INTO source_recovery_entries(project_id,recovery_id,ordinal,path,mode) SELECT id,?,0,'.',448 FROM projects LIMIT 1`, id)
				testutil.FailErr(t, "seed released recovery", err)
			}
			_, err = source.Exec(`INSERT INTO source_history_entries(id,project_id,kind,state,undo_label,redo_label,undo_plan_json,redo_plan_json,created_at,updated_at) SELECT 'recovery-history',id,'trash','undone','','','{}','{"recovery_id":"retained"}','','' FROM projects LIMIT 1`)
			testutil.FailErr(t, "seed redo ownership", err)
			before := namespaceTableContents(t, source, "source_recovery_entries")
			testutil.FailErr(t, "close released store", source.Close())
			for boot := 0; boot < 2; boot++ {
				testutil.FailErr(t, "upgrade released recovery", UpgradeStaged(t.Context(), path))
				reader, err := openReader(t.Context(), path)
				testutil.FailErr(t, "open upgraded store", err)
				if got := namespaceTableContents(t, reader, "source_recovery_entries"); got != before {
					t.Fatal("upgrade changed retained manifest")
				}
				var queued int
				testutil.FailErr(t, "read reclamation queue", reader.QueryRow(`SELECT count(*) FROM source_recovery_reclaim_queue WHERE recovery_id IN ('orphan','retained')`).Scan(&queued))
				if queued != 2 {
					t.Fatalf("queued %d manifests, want 2", queued)
				}
				testutil.FailErr(t, "close upgraded store", reader.Close())
			}
		})
	}
}
