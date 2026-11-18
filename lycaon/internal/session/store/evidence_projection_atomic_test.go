package store

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUntrustedProjectionFailureRollsBackEvidence(t *testing.T) {
	for name, commit := range map[string]func(context.Context, *SQL, *api.Session) error{
		"inherited trust": func(ctx context.Context, st *SQL, sess *api.Session) error {
			return st.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{Handle: evidence.InheritUntrustedHandle, Kind: "web", Shape: evidence.ShapeURL, URL: "https://example.invalid/"})
		},
		"program network provenance": func(ctx context.Context, st *SQL, sess *api.Session) error {
			_, err := st.CommitProgramHTTPProvenance(ctx, sess.ID, sess.WorkspacePath, evidence.ProgramHTTPProvenance{
				UntrustedContent: true, ObservedURLs: []string{"https://example.invalid/"},
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := newSQLTestStore(t)
			ctx := t.Context()
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			if st.SessionUntrustedContent(sess.ID) {
				t.Fatal("new session is untrusted")
			}
			_, err = st.DB().ExecContext(ctx, `CREATE TRIGGER reject_projection BEFORE INSERT ON evidence_index BEGIN SELECT RAISE(ABORT, 'projection rejected'); END`)
			testutil.FailErr(t, "install projection failure", err)
			if err := commit(ctx, st, sess); err == nil {
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
			testutil.FailErr(t, "retry evidence publication", commit(ctx, st, sess))
			var count int
			err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_index WHERE session_id = ? AND untrusted = 1`, sess.ID).Scan(&count)
			testutil.FailErr(t, "read trust projection", err)
			if count != 1 || !st.SessionUntrustedContent(sess.ID) {
				t.Fatalf("retry did not publish evidence and trust projection: rows=%d", count)
			}
		})
	}
}
