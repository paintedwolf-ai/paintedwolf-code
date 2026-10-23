package db

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedTreeUpgradePreservesManifestPlansAndRefusesUnknownBindings(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "manifest", true: "unknown binding"}[unknown], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			source, err := Open(path)
			testutil.FailErr(t, "create known intermediate", err)
			_, err = source.ExecContext(t.Context(), `INSERT INTO projects(id,last_opened_at,created_at) VALUES('project','','')`)
			testutil.FailErr(t, "seed project", err)
			plan := `{"kind":"delete","recovery_id":"manifest","recovery_count":2,"before_size":4}`
			if unknown {
				plan = `{"kind":"delete","retained_tree":{"version":99}}`
			}
			_, err = source.ExecContext(t.Context(), `INSERT INTO source_mutations(id,project_id,kind,input_digest,plan_json,status,error,created_at,updated_at) VALUES('operation','project','delete','digest',?,'prepared','','','')`, plan)
			testutil.FailErr(t, "seed durable plan", err)
			_, err = source.ExecContext(t.Context(), `PRAGMA user_version=6`)
			testutil.FailErr(t, "claim known intermediate", err)
			testutil.FailErr(t, "close source", source.Close())
			err = UpgradeStaged(t.Context(), path)
			if unknown && err == nil {
				t.Fatal("unknown retained binding was upgraded")
			}
			if !unknown {
				testutil.FailErr(t, "upgrade manifest", err)
				testutil.FailErr(t, "restart upgraded store", UpgradeStaged(t.Context(), path))
			}
			reader, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "read result", err)
			defer reader.Close()
			var version int
			var raw string
			testutil.FailErr(t, "read revision", reader.QueryRow(`PRAGMA user_version`).Scan(&version))
			testutil.FailErr(t, "read retained plan", reader.QueryRow(`SELECT plan_json FROM source_mutations WHERE id='operation'`).Scan(&raw))
			if unknown {
				if version != 6 || raw != plan {
					t.Fatalf("refused upgrade changed source: revision=%d plan=%s", version, raw)
				}
				return
			}
			var decoded map[string]any
			testutil.FailErr(t, "decode upgraded plan", json.Unmarshal([]byte(raw), &decoded))
			if version != SchemaVersion || decoded["recovery_id"] != "manifest" || decoded["recovery_count"] != float64(2) || decoded["before_size"] != float64(4) || decoded["retained_tree"] != nil {
				t.Fatalf("migration changed the manifest contract: revision=%d plan=%s", version, raw)
			}
		})
	}
}
