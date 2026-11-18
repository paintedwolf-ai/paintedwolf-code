package store

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUntrustedProjectionFailureRollsBackEvidence(t *testing.T) {
	st := newSQLTestStore(t)
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if st.SessionUntrustedContent(sess.ID) {
		t.Fatal("new session is untrusted")
	}
	_, err = st.DB().ExecContext(ctx, `CREATE TRIGGER reject_projection BEFORE INSERT ON evidence_index BEGIN SELECT RAISE(ABORT, 'projection rejected'); END`)
	testutil.FailErr(t, "install projection failure", err)
	rec := evidence.Record{Handle: evidence.InheritUntrustedHandle, Kind: "web", Shape: evidence.ShapeURL, URL: "https://example.invalid/"}
	if err := st.UpsertEvidenceRecord(ctx, sess.ID, rec); err == nil {
		t.Fatal("projection failure did not abort publication")
	}
	ledger, err := st.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "read rolled back ledger", err)
	if len(ledger.Handles) != 0 {
		t.Fatalf("evidence persisted after projection failure: %+v", ledger.Handles)
	}
	if st.SessionUntrustedContent(sess.ID) || NewSQL(st.db).SessionUntrustedContent(sess.ID) {
		t.Fatal("failed publication changed trust state")
	}
	_, err = st.DB().ExecContext(ctx, `DROP TRIGGER reject_projection`)
	testutil.FailErr(t, "remove projection failure", err)
	testutil.FailErr(t, "retry evidence publication", st.UpsertEvidenceRecord(ctx, sess.ID, rec))
	var count int
	err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_index WHERE session_id = ? AND untrusted = 1`, sess.ID).Scan(&count)
	testutil.FailErr(t, "read trust projection", err)
	if count != 1 || !st.SessionUntrustedContent(sess.ID) {
		t.Fatalf("retry did not publish evidence and trust projection: rows=%d", count)
	}
}

func TestProgramProvenanceFailureRollsBackAllRecords(t *testing.T) {
	st := newSQLTestStore(t)
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if st.SessionUntrustedContent(sess.ID) {
		t.Fatal("new session is untrusted")
	}
	_, err = st.DB().ExecContext(ctx, `CREATE TRIGGER reject_custody BEFORE INSERT ON evidence_records WHEN NEW.kind = 'write' BEGIN SELECT RAISE(ABORT, 'custody rejected'); END`)
	testutil.FailErr(t, "install custody failure", err)
	commit := func() error {
		_, err := st.CommitProgramHTTPProvenance(ctx, sess.ID, sess.WorkspacePath, evidence.ProgramHTTPProvenance{
			UntrustedContent: true, ObservedURLs: []string{"https://example.invalid/"}, ClearedPaths: []string{"replaced.go"},
		})
		return err
	}
	if err := commit(); err == nil {
		t.Fatal("second record failure did not abort publication")
	}
	ledger, err := st.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "read rolled back ledger", err)
	if len(ledger.Handles) != 0 || st.SessionUntrustedContent(sess.ID) || NewSQL(st.db).SessionUntrustedContent(sess.ID) {
		t.Fatal("failed publication retained records or trust state")
	}
	_, err = st.DB().ExecContext(ctx, `DROP TRIGGER reject_custody`)
	testutil.FailErr(t, "remove custody failure", err)
	testutil.FailErr(t, "retry provenance publication", commit())
	ledger, err = st.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "read committed ledger", err)
	if len(ledger.Handles) != 2 || !st.SessionUntrustedContent(sess.ID) || !NewSQL(st.db).SessionUntrustedContent(sess.ID) {
		t.Fatal("retry did not publish both records and trust state")
	}
}
