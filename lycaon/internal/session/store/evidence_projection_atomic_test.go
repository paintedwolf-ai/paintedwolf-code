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
	sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if st.Evidence.SessionUntrustedContent(sess.ID) {
		t.Fatal("new session is untrusted")
	}
	_, err = st.DB().ExecContext(ctx, `CREATE TRIGGER reject_projection BEFORE INSERT ON evidence_index BEGIN SELECT RAISE(ABORT, 'projection rejected'); END`)
	testutil.FailErr(t, "install projection failure", err)
	rec := evidence.Record{Handle: evidence.InheritUntrustedHandle, Kind: "web", Shape: evidence.ShapeURL, URL: "https://example.invalid/"}
	if err := st.Evidence.UpsertEvidenceRecord(ctx, sess.ID, rec); err == nil {
		t.Fatal("projection failure did not abort publication")
	}
	ledger, err := st.Evidence.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "read rolled back ledger", err)
	if len(ledger.Handles) != 0 {
		t.Fatalf("evidence persisted after projection failure: %+v", ledger.Handles)
	}
	if st.Evidence.SessionUntrustedContent(sess.ID) || NewSQL(st.core.db).Evidence.SessionUntrustedContent(sess.ID) {
		t.Fatal("failed publication changed trust state")
	}
	_, err = st.DB().ExecContext(ctx, `DROP TRIGGER reject_projection`)
	testutil.FailErr(t, "remove projection failure", err)
	testutil.FailErr(t, "retry evidence publication", st.Evidence.UpsertEvidenceRecord(ctx, sess.ID, rec))
	var count int
	err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_index WHERE session_id = ? AND untrusted = 1`, sess.ID).Scan(&count)
	testutil.FailErr(t, "read trust projection", err)
	if count != 1 || !st.Evidence.SessionUntrustedContent(sess.ID) {
		t.Fatalf("retry did not publish evidence and trust projection: rows=%d", count)
	}
}

func TestProgramProvenanceFailureRollsBackAllRecords(t *testing.T) {
	st := newSQLTestStore(t)
	ctx := t.Context()
	sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if st.Evidence.SessionUntrustedContent(sess.ID) {
		t.Fatal("new session is untrusted")
	}
	_, err = st.DB().ExecContext(ctx, `CREATE TRIGGER reject_custody BEFORE INSERT ON evidence_records WHEN NEW.kind = 'write' BEGIN SELECT RAISE(ABORT, 'custody rejected'); END`)
	testutil.FailErr(t, "install custody failure", err)
	commit := func() error {
		_, err := st.Evidence.CommitProgramHTTPProvenance(ctx, evidence.ProgramProvenanceTarget{SessionID: sess.ID, ProjectDir: sess.WorkspacePath}, evidence.ProgramHTTPProvenance{
			UntrustedContent: true, ObservedURLs: []string{"https://example.invalid/"}, ClearedPaths: []string{"replaced.go"},
		})
		return err
	}
	if err := commit(); err == nil {
		t.Fatal("second record failure did not abort publication")
	}
	ledger, err := st.Evidence.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "read rolled back ledger", err)
	if len(ledger.Handles) != 0 || st.Evidence.SessionUntrustedContent(sess.ID) || NewSQL(st.core.db).Evidence.SessionUntrustedContent(sess.ID) {
		t.Fatal("failed publication retained records or trust state")
	}
	_, err = st.DB().ExecContext(ctx, `DROP TRIGGER reject_custody`)
	testutil.FailErr(t, "remove custody failure", err)
	testutil.FailErr(t, "retry provenance publication", commit())
	ledger, err = st.Evidence.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "read committed ledger", err)
	if len(ledger.Handles) != 2 || !st.Evidence.SessionUntrustedContent(sess.ID) || !NewSQL(st.core.db).Evidence.SessionUntrustedContent(sess.ID) {
		t.Fatal("retry did not publish both records and trust state")
	}
}

func TestProgramProvenanceRollsBackBothSessionLedgers(t *testing.T) {
	ctx := t.Context()
	st := newSQLTestStore(t)
	chat, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create chat", err)
	child, err := st.Sessions.CreateChild(ctx, chat, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)

	_, err = st.DB().ExecContext(ctx, `CREATE TRIGGER reject_chat_custody BEFORE INSERT ON evidence_records
		WHEN NEW.session_id IN (SELECT id FROM sessions WHERE parent_session_id IS NULL)
		BEGIN SELECT RAISE(ABORT, 'chat custody rejected'); END`)
	testutil.FailErr(t, "install chat write failure", err)
	target := evidence.ProgramProvenanceTarget{SessionID: child.ID, ChatSessionID: chat.ID, ProjectDir: child.WorkspacePath}
	const url = "https://example.invalid/file"
	prov := evidence.ProgramHTTPProvenance{ProgramID: "program:atomic", UntrustedContent: true, ObservedURLs: []string{url}, CustodyPaths: []string{"file.txt"}}
	if _, err := st.Evidence.CommitProgramHTTPProvenance(ctx, target, prov); err == nil {
		t.Fatal("chat failure did not abort publication")
	}
	for _, sessionID := range []string{chat.ID, child.ID} {
		ledger, err := st.Evidence.LoadLedger(ctx, sessionID)
		testutil.FailErr(t, "read rolled back ledger", err)
		if len(ledger.Handles) != 0 || st.Evidence.SessionUntrustedContent(sessionID) {
			t.Fatalf("failed publication changed %s", sessionID)
		}
	}

	_, err = st.DB().ExecContext(ctx, `DROP TRIGGER reject_chat_custody`)
	testutil.FailErr(t, "remove chat write failure", err)
	_, err = st.Evidence.CommitProgramHTTPProvenance(ctx, target, prov)
	testutil.FailErr(t, "commit both ledgers", err)
	for _, sessionID := range []string{chat.ID, child.ID} {
		urls, err := st.Evidence.ExternalCustodyURLsForPaths(ctx, sessionID, target.ProjectDir, []string{"file.txt"})
		testutil.FailErr(t, "read committed custody", err)
		if len(urls) != 1 || urls[0] != url {
			t.Fatalf("custody for %s = %v", sessionID, urls)
		}
	}
	if !st.Evidence.SessionUntrustedContent(child.ID) || st.Evidence.SessionUntrustedContent(chat.ID) {
		t.Fatal("shared custody marked the chat as having ingested the child output")
	}
}
