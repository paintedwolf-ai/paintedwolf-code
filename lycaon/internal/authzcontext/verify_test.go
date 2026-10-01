package authzcontext_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestVerify_eventReorderBreaks(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	ledger := &authzcontext.Ledger{Store: mem}
	for i := 0; i < 3; i++ {
		ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
			SessionID:  "sess-reorder",
			Action:     authzcontext.EventActionToolDenied,
			Outcome:    authzcontext.EventOutcomeDenied,
			ResolvedBy: authzcontext.ResolvedBySystemDeny,
			Detail:     authzcontext.DetailInput{Tool: "command"},
		})
	}
	rows, err := mem.ListEvents(context.Background(), "sess-reorder")
	if err != nil || len(rows) != 3 {
		t.Fatalf("events = %d err = %v", len(rows), err)
	}
	if br := authzcontext.VerifyEvents("sess-reorder", rows); br != nil {
		t.Fatalf("verify before reorder: %+v", br)
	}
	reordered := []authzcontext.Event{rows[1], rows[0], rows[2]}
	if br := authzcontext.VerifyEvents("sess-reorder", reordered); br == nil {
		t.Fatal("expected verify break after reorder")
	}
	if br := authzcontext.VerifyEvents("sess-reorder", reordered); br.Table != authzcontext.ChainTableEvents {
		t.Fatalf("table = %q want authz_events", br.Table)
	}
}

func TestVerify_sqlEventTamperAndReorder(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	store := authzcontext.NewSQLStore(sqlDB)
	ledger := &authzcontext.Ledger{Store: store}
	insertSession(t, sqlDB, "sess-sql-reorder", time.Now().UTC())
	for i := 0; i < 3; i++ {
		ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
			SessionID:  "sess-sql-reorder",
			Action:     authzcontext.EventActionToolDenied,
			Outcome:    authzcontext.EventOutcomeDenied,
			ResolvedBy: authzcontext.ResolvedBySystemDeny,
			Detail:     authzcontext.DetailInput{Tool: "command"},
		})
	}
	br, err := authzcontext.Verify(context.Background(), sqlDB)
	if err != nil || br != nil {
		t.Fatalf("verify before tamper br=%+v err=%v", br, err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `DROP TRIGGER authz_events_immutable`); err != nil {
		testutil.FailErr(t, "drop trigger", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `UPDATE authz_events SET row_hash = '00' WHERE session_id = ? AND event_seq = 2`, "sess-sql-reorder"); err != nil {
		testutil.FailErr(t, "tamper row_hash", err)
	}
	br, err = authzcontext.Verify(context.Background(), sqlDB)
	if err != nil {
		testutil.FailErr(t, "verify after tamper", err)
	}
	if br == nil || br.Table != authzcontext.ChainTableEvents {
		t.Fatalf("want event chain break, got br=%+v", br)
	}

	if _, err := sqlDB.ExecContext(context.Background(), `
		UPDATE authz_events SET event_seq = 999 WHERE session_id = ? AND event_seq = 1
	`, "sess-sql-reorder"); err != nil {
		testutil.FailErr(t, "stage reorder", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `
		UPDATE authz_events SET event_seq = 1 WHERE session_id = ? AND event_seq = 2
	`, "sess-sql-reorder"); err != nil {
		testutil.FailErr(t, "reorder step 2", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `
		UPDATE authz_events SET event_seq = 2 WHERE session_id = ? AND event_seq = 999
	`, "sess-sql-reorder"); err != nil {
		testutil.FailErr(t, "reorder step 3", err)
	}
	br, err = authzcontext.Verify(context.Background(), sqlDB)
	if err != nil {
		testutil.FailErr(t, "verify after reorder", err)
	}
	if br == nil {
		t.Fatal("expected verify break after reorder")
	}
}

func TestListEvents_newestFirstLimit(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	ledger := &authzcontext.Ledger{Store: mem}
	for i := 0; i < 5; i++ {
		ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
			SessionID:  "sess-list",
			Action:     authzcontext.EventActionToolDenied,
			Outcome:    authzcontext.EventOutcomeDenied,
			ResolvedBy: authzcontext.ResolvedBySystemDeny,
			Detail:     authzcontext.DetailInput{Tool: "command"},
		})
	}
	got, err := authzcontext.ListEvents(context.Background(), mem, "sess-list", 2)
	if err != nil {
		testutil.FailErr(t, "list", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d want 2", len(got))
	}
	if got[0].EventSeq != 5 || got[1].EventSeq != 4 {
		t.Fatalf("order = [%d,%d] want newest first [5,4]", got[0].EventSeq, got[1].EventSeq)
	}
}

func TestExportSessionAuthz_jsonlHead(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	sealer := &authzcontext.Sealer{Store: mem}
	sess := &api.Session{ID: "sess-export", ProjectID: "p1", AgentType: "implementer", Posture: api.SessionPostureBuild}
	if err := sealer.Seal(context.Background(), sess, "implement", ""); err != nil {
		testutil.FailErr(t, "seal", err)
	}
	ledger := &authzcontext.Ledger{Store: mem}
	ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
		SessionID:  "sess-export",
		Action:     authzcontext.EventActionToolDenied,
		Outcome:    authzcontext.EventOutcomeDenied,
		ResolvedBy: authzcontext.ResolvedBySystemDeny,
		Detail:     authzcontext.DetailInput{Tool: "command"},
	})

	var buf bytes.Buffer
	if err := authzcontext.ExportSessionAuthz(context.Background(), &buf, mem, "sess-export"); err != nil {
		testutil.FailErr(t, "export", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d want context+event+head", len(lines))
	}
	var head map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &head); err != nil {
		testutil.FailErr(t, "head json", err)
	}
	if head["kind"] != "head" {
		t.Fatalf("last line kind = %v", head["kind"])
	}
	if head["context_head_hash"] == "" || head["event_head_hash"] == "" {
		t.Fatalf("head hashes missing: %+v", head)
	}
}

func TestSQLStore_contextTamperTable(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	store := authzcontext.NewSQLStore(sqlDB)
	insertSession(t, sqlDB, "sess-ctx-tamper", time.Now().UTC())
	c := authzcontext.Assemble(authzcontext.AssembleInput{
		Session:   &api.Session{ID: "sess-ctx-tamper", ProjectID: "p1"},
		ProfileID: "implement",
		Profile:   sandbox.ToolProfile{ID: "implement", Tools: map[string]bool{"read": true}},
		Perms:     settings.ApprovalConfig{Posture: gate.PostureBalanced},
	})
	if _, err := store.AppendContext(context.Background(), c); err != nil {
		testutil.FailErr(t, "append", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `DROP TRIGGER authorization_contexts_immutable`); err != nil {
		testutil.FailErr(t, "drop trigger", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `UPDATE authorization_contexts SET row_hash = '00' WHERE session_id = ?`, "sess-ctx-tamper"); err != nil {
		testutil.FailErr(t, "tamper", err)
	}
	br, err := authzcontext.Verify(context.Background(), sqlDB)
	if err != nil {
		testutil.FailErr(t, "verify", err)
	}
	if br == nil || br.Table != authzcontext.ChainTableContexts {
		t.Fatalf("want context break, got %+v", br)
	}
}

func TestVerifyBindsTheResolvingPersonIntoTheChain(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	ledger := &authzcontext.Ledger{Store: mem}
	for _, resolver := range []string{"person-a", "person-b"} {
		ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
			SessionID:        "sess-resolver",
			Action:           authzcontext.EventActionApprovalDecision,
			Outcome:          authzcontext.EventOutcomeAllowed,
			ResolvedBy:       authzcontext.ResolvedByHuman,
			ResolverPersonID: resolver,
			Detail:           authzcontext.DetailInput{Tool: "command"},
		})
	}
	rows, err := mem.ListEvents(context.Background(), "sess-resolver")
	testutil.FailErr(t, "list events", err)
	if len(rows) != 2 || rows[0].ResolverPersonID != "person-a" || rows[1].ResolverPersonID != "person-b" {
		t.Fatalf("events = %+v", rows)
	}
	if br := authzcontext.VerifyEvents("sess-resolver", rows); br != nil {
		t.Fatalf("verify before tamper: %+v", br)
	}
	rows[0].ResolverPersonID = "person-b"
	if br := authzcontext.VerifyEvents("sess-resolver", rows); br == nil {
		t.Fatal("changing the resolving person did not break the chain")
	}
}
