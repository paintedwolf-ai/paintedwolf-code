package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleasedVerdictPublicationUpgrade(t *testing.T) {
	for _, version := range []string{"1.0.0", "1.0.1"} {
		t.Run(version, func(t *testing.T) {
			original, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", version, "store.db"))
			testutil.FailErr(t, "read released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, original, 0600))
			source, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "open released store", err)
			defer source.Close()
			cases := []struct {
				id, status, response string
				published            bool
			}{
				{"prepared", "prepared", "", false},
				{"applied", "evidence_applied", "", true},
				{"nonterminal", "committed", `{"Valid":true,"Attempt":1}`, true},
				{"terminal", "committed", `{"Valid":true,"Terminal":true,"Attempt":2}`, true},
				{"rejected", "committed", `{"Valid":false,"Attempt":2}`, true},
				{"diverged", "diverged", "", false},
			}
			for _, c := range cases {
				_, err = source.Exec(`INSERT INTO workflow_verdict_operations(tool_call_id,run_id,source_revision,phase,input_digest,evidence_record_id,evidence_json,status,response_json,error,created_at,updated_at)
 SELECT ?,id,1,current_phase,'digest',?,'{}',?,?,'','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z' FROM workflow_runs LIMIT 1`, c.id, c.id, c.status, c.response)
				testutil.FailErr(t, "seed released verdict", err)
			}
			rows, err := source.Query(`SELECT tool_call_id,run_id,source_revision,phase,input_digest,evidence_record_id,evidence_json,status,response_json,error,created_at,updated_at FROM workflow_verdict_operations`)
			testutil.FailErr(t, "read released receipts", err)
			before := retainedRows(t, rows)
			testutil.FailErr(t, "close released store", source.Close())
			for boot := 0; boot < 2; boot++ {
				testutil.FailErr(t, "upgrade staged store", UpgradeStaged(t.Context(), path))
				reader, err := openReader(t.Context(), path)
				testutil.FailErr(t, "open upgraded store", err)
				rows, err := reader.Query(`SELECT tool_call_id,run_id,source_revision,phase,input_digest,evidence_record_id,evidence_json,status,response_json,error,created_at,updated_at FROM workflow_verdict_operations`)
				testutil.FailErr(t, "read retained receipts", err)
				if !reflect.DeepEqual(before, retainedRows(t, rows)) {
					t.Fatal("upgrade changed released receipts")
				}
				for _, c := range cases {
					var published bool
					testutil.FailErr(t, "read publication acknowledgement", reader.QueryRow(`SELECT evidence_published FROM workflow_verdict_operations WHERE tool_call_id=?`, c.id).Scan(&published))
					if published != c.published {
						t.Errorf("%s publication=%v, want %v", c.id, published, c.published)
					}
				}
				pending, err := New(reader).ListPendingWorkflowVerdictOperations(t.Context())
				testutil.FailErr(t, "read recovery backlog", err)
				if len(pending) != 2 || pending[0].ToolCallID != "applied" || pending[1].ToolCallID != "prepared" {
					t.Fatalf("unexpected recovery backlog: %+v", pending)
				}
				testutil.FailErr(t, "close upgraded store", reader.Close())
			}
		})
	}
}
