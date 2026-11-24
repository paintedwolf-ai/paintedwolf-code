package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

const revision5LedgerID = "source-manifests-and-command-records"

// releasedRevision4Store copies the 1.0.2 corpus, the newest shipped shape, and
// adds a project and session owned by its recorded person.
func releasedRevision4Store(t *testing.T, path string) *sql.DB {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDir(), "1.0.2", "store.db"))
	testutil.FailErr(t, "read released store", err)
	testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0o600))
	source, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released store", err)
	_, err = source.ExecContext(t.Context(), `
		INSERT INTO projects(id,last_opened_at,created_at) VALUES('fixture-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('fixture-session','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`)
	testutil.FailErr(t, "seed released session", err)
	released, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect released store", err)
	if released != migrations.Revision5(released).From {
		t.Fatalf("released store is %+v, not the registered revision-4 source", released)
	}
	return source
}

func TestRevision5UpgradesEveryReleasedStore(t *testing.T) {
	testutil.SkipIfRace(t, "single-goroutine corpus replay; test:full runs it")
	for _, version := range []string{"1.0.0", "1.0.1", "1.0.2"} {
		t.Run(version, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(corpusDir(), version, "store.db"))
			testutil.FailErr(t, "read immutable released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0o600))
			released, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "open released store", err)
			var roots int
			testutil.FailErr(t, "count released roots", released.QueryRow(`SELECT count(*) FROM project_roots`).Scan(&roots))
			testutil.FailErr(t, "close released store", released.Close())
			testutil.FailErr(t, "upgrade complete registered route", UpgradeStaged(t.Context(), path))
			testutil.FailErr(t, "reopen complete registered route", UpgradeStaged(t.Context(), path))
			upgraded, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "read upgraded released store", err)
			defer upgraded.Close()
			diff, err := BaselineShapeDiff(t.Context(), upgraded)
			testutil.FailErr(t, "compare released route with complete fresh target", err)
			if len(diff) != 0 {
				t.Fatalf("released route shape differs: %v", diff)
			}
			var count int
			testutil.FailErr(t, "read migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id=? AND from_revision=4 AND to_revision=5`, revision5LedgerID).Scan(&count))
			if count != 1 {
				t.Fatalf("revision 5 applied %d times", count)
			}
			testutil.FailErr(t, "no fabricated executions", upgraded.QueryRow(`SELECT count(*) FROM invocation_program_executions`).Scan(&count))
			if count != 0 {
				t.Fatal("upgrade fabricated host executions")
			}
			assertOutsideUpgrade(t, upgraded, roots)
		})
	}
}

func TestRevision5KeepsRetainedAuthorityStructured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := releasedRevision4Store(t, path)
	const retained = `{"approval_plan":{"id":"plan","action_digest":"semantic","stage":"pre_dial","recommended_option_id":"once","options":[{"id":"once","authority":[{"kind":"direct_ip_permit","session_id":"fixture-session","tool_call_id":"actual-model-call","action_digest":"semantic","direct_ip_lease":{"ActionDigest":"semantic","RequestDigest":"requested","ConfinementDigest":"confined","ChatConfinementDigest":"","DeclaredDestinations":["fixture.invalid"],"CommandSummary":"fixture"}}]}]}}`
	_, err := source.ExecContext(t.Context(), `INSERT INTO checkpoints(id,session_id,project_id,kind,status,created_at,payload_json) VALUES('fixture-checkpoint','fixture-session','fixture-project','tool_approval','pending','2026-01-01T00:00:00Z',?)`, retained)
	testutil.FailErr(t, "seed retained once metadata", err)
	testutil.FailErr(t, "close source", source.Close())
	testutil.FailErr(t, "upgrade released source", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "read upgraded store", err)
	defer upgraded.Close()
	var raw string
	testutil.FailErr(t, "read migrated checkpoint", upgraded.QueryRow(`SELECT payload_json FROM checkpoints WHERE id='fixture-checkpoint'`).Scan(&raw))
	var payload struct {
		Plan struct {
			Options []struct {
				Authority []struct {
					ExecutionID  *string `json:"execution_id"`
					ToolCallID   string  `json:"tool_call_id"`
					ActionDigest string  `json:"action_digest"`
				} `json:"authority"`
			} `json:"options"`
		} `json:"approval_plan"`
	}
	testutil.FailErr(t, "decode migrated checkpoint", json.Unmarshal([]byte(raw), &payload))
	delta := payload.Plan.Options[0].Authority[0]
	if delta.ExecutionID == nil || *delta.ExecutionID != "" || delta.ToolCallID != "actual-model-call" || delta.ActionDigest != "semantic" {
		t.Fatal("retained authority acquired native identity or changed model meaning")
	}
	var format, digest, body string
	testutil.FailErr(t, "read retained authority columns", upgraded.QueryRow(`SELECT authority_format,authority_digest,authority_body FROM checkpoints WHERE id='fixture-checkpoint'`).Scan(&format, &digest, &body))
	if format != "" || digest != "" || body != "" {
		t.Fatal("retained structured authority acquired a native format")
	}
}

func TestRevision5AdmitsLinksAndKeepsSourceIdentities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := releasedRevision4Store(t, path)
	_, err := source.ExecContext(t.Context(), `INSERT INTO source_files(id,project_id,entry_kind,created_ts) VALUES('kept-file','fixture-project','file','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed source identity", err)
	if _, err := source.ExecContext(t.Context(), `INSERT INTO source_files(id,project_id,entry_kind,created_ts) VALUES('early-link','fixture-project','link','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("revision 4 accepted a link entry")
	}
	testutil.FailErr(t, "close source", source.Close())
	testutil.FailErr(t, "upgrade released source", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer upgraded.Close()
	var kind string
	testutil.FailErr(t, "read retained source identity", upgraded.QueryRow(`SELECT entry_kind FROM source_files WHERE id='kept-file'`).Scan(&kind))
	if kind != "file" {
		t.Fatalf("retained entry kind = %q", kind)
	}
	_, err = upgraded.Exec(`INSERT INTO source_files(id,project_id,entry_kind,created_ts) VALUES('new-link','fixture-project','link','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "record link entry after upgrade", err)
}

func TestRevision5ProgressRowsUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := releasedRevision4Store(t, path)
	longLabel := strings.Repeat("x", 600)
	_, err := source.ExecContext(t.Context(), `
		INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES
			('sess-goal-a','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
			('sess-goal-b','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
			('sess-checklist','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
			('sess-long','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
			('sess-empty','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO session_progress(session_id,workflow_run_id,content,updated_at) VALUES
			('sess-goal-a','run-a',char(35,32)||'Goal'||char(10,10)||'Goal A'||char(10,10,35,35,32)||'Progress'||char(10)||'- [ ] Shared step','2026-01-01T00:00:00Z'),
			('sess-goal-b','run-b',char(35,32)||'Goal'||char(10,10)||'Goal B'||char(10,10,35,35,32)||'Progress'||char(10)||'- [ ] Shared step','2026-01-01T00:00:00Z'),
			('sess-checklist','run-1',char(35,32)||'Goal'||char(10,10)||'Ship'||char(10,10,35,35,32)||'Progress'||char(10)||'- [x] First'||char(10)||'- [ ] Second'||char(10)||'- [~] Third'||char(10)||'- [>] Optional','2026-01-01T00:00:00Z'),
			('sess-long','run-long','- [ ] '||?,'2026-01-01T00:00:00Z'),
			('sess-empty','run-empty',char(35,32)||'Goal'||char(10,10)||'Empty'||char(10,10,35,35,32)||'Progress'||char(10),'2026-01-01T00:00:00Z');`,
		longLabel)
	testutil.FailErr(t, "seed released progress rows", err)
	testutil.FailErr(t, "close source", source.Close())
	testutil.FailErr(t, "upgrade released source", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "restart upgraded store", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer upgraded.Close()

	var docVer, nextSeq int64
	testutil.FailErr(t, "read migrated checklist header", upgraded.QueryRow(`SELECT document_version, next_item_seq FROM session_progress WHERE session_id='sess-checklist'`).Scan(&docVer, &nextSeq))
	if docVer != 1 || nextSeq != 5 {
		t.Fatalf("checklist header docVer=%d nextSeq=%d, want 1 and 5", docVer, nextSeq)
	}
	type itemRow struct {
		id, state, label string
		ord              int64
	}
	readItems := func(sessID string) []itemRow {
		rows, qErr := upgraded.Query(`SELECT item_id, ord, state, label FROM session_progress_items WHERE session_id=? ORDER BY ord`, sessID)
		testutil.FailErr(t, "query items "+sessID, qErr)
		defer rows.Close()
		var out []itemRow
		for rows.Next() {
			var r itemRow
			testutil.FailErr(t, "scan item "+sessID, rows.Scan(&r.id, &r.ord, &r.state, &r.label))
			out = append(out, r)
		}
		testutil.FailErr(t, "rows err "+sessID, rows.Err())
		return out
	}
	wantChecklist := []itemRow{
		{id: "p1", ord: 0, state: "done", label: "First"},
		{id: "p2", ord: 1, state: "pending", label: "Second"},
		{id: "p3", ord: 2, state: "na", label: "Third"},
		{id: "p4", ord: 3, state: "optional", label: "Optional"},
	}
	if got := readItems("sess-checklist"); len(got) != len(wantChecklist) {
		t.Fatalf("checklist items=%+v, want %+v", got, wantChecklist)
	} else {
		for i := range wantChecklist {
			if got[i] != wantChecklist[i] {
				t.Fatalf("checklist[%d]=%+v, want %+v", i, got[i], wantChecklist[i])
			}
		}
	}
	for _, sessID := range []string{"sess-goal-a", "sess-goal-b"} {
		got := readItems(sessID)
		if len(got) != 1 || got[0] != (itemRow{id: "p1", ord: 0, state: "pending", label: "Shared step"}) {
			t.Fatalf("%s items=%+v", sessID, got)
		}
	}
	gotLong := readItems("sess-long")
	if len(gotLong) != 1 || gotLong[0].label != longLabel {
		t.Fatalf("long label truncated: len=%d want %d", len(gotLong[0].label), len(longLabel))
	}
	testutil.FailErr(t, "read empty checklist header", upgraded.QueryRow(`SELECT document_version, next_item_seq FROM session_progress WHERE session_id='sess-empty'`).Scan(&docVer, &nextSeq))
	if docVer != 1 || nextSeq != 1 || len(readItems("sess-empty")) != 0 {
		t.Fatalf("empty checklist docVer=%d nextSeq=%d", docVer, nextSeq)
	}
}

// A store claiming revision 5 with the revision-4 shape is an unknown shape:
// planning refuses it and leaves its bytes untouched.
func TestRevision5ClaimWithoutItsShapeIsRefusedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := releasedRevision4Store(t, path)
	released, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect released store", err)
	_, err = source.ExecContext(t.Context(), `PRAGMA user_version=5`)
	testutil.FailErr(t, "claim revision 5", err)
	testutil.FailErr(t, "close source", source.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read source bytes", err)
	var incompatible *StoreIncompatibleError
	if err := UpgradeStaged(t.Context(), path); !errors.As(err, &incompatible) || incompatible.Reason != RecoveryReasonSchemaMismatch || incompatible.StoreSchemaVersion != 5 {
		t.Fatalf("revision 5 claim was not refused as a schema mismatch: %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused bytes", err)
	if !bytes.Equal(before, after) {
		t.Fatal("refused store was mutated")
	}
	if _, err := PlanSchemaUpgrade(t.Context(), migrations.Baseline{Revision: 5, Shape: released.Shape}); !errors.Is(err, migrations.ErrUnsupported) {
		t.Fatalf("revision 5 claim planned: %v", err)
	}
}
