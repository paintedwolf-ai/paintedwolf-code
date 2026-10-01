package authzcontext_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLedger_appendGateFailClosed(t *testing.T) {
	ledger := &authzcontext.Ledger{Store: authzcontext.FailStore{}}
	err := ledger.AppendGate(context.Background(), authzcontext.RecordInput{
		SessionID:  "sess",
		Action:     authzcontext.EventActionApprovalDecision,
		Outcome:    authzcontext.EventOutcomeAllowed,
		ResolvedBy: authzcontext.ResolvedByHuman,
		ToolName:   "command",
		Detail:     authzcontext.DetailInput{Tool: "command"},
	})
	if err == nil || !errors.Is(err, authzledger.ErrSealFailed) {
		t.Fatalf("want ErrSealFailed, got %v", err)
	}
}

func TestLedger_appendRecordBestEffort(t *testing.T) {
	ledger := &authzcontext.Ledger{Store: authzcontext.FailStore{}}
	ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
		SessionID:  "sess",
		Action:     authzcontext.EventActionToolDenied,
		Outcome:    authzcontext.EventOutcomeDenied,
		ResolvedBy: authzcontext.ResolvedBySystemDeny,
	})
}

func TestLedgerApprovalDecisionCarriesApprovalRuleProvenance(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	insertSession(t, sqlDB, "sess-policy", time.Now().UTC())
	recorder := authzcontext.SQLRecorder(sqlDB)
	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "append gate tx", recorder.AppendApprovalGateTx(t.Context(), tx, authzledger.ApprovalDecisionRecord{
		SessionID: "sess-policy", ToolCallID: "call-policy", CheckpointID: "cp-1", PlanID: "plan-1", ActionDigest: "action-1",
		SelectedOptionID: "deny", GrantIDs: []string{"grant-1"}, SubjectKind: "command", SubjectTitle: "git push",
		Tool: "command", Outcome: authzledger.OutcomeDenied,
		Gate: "user_rule", Reasons: []string{"user_rule", "sensitive_location"},
		ApprovalRules: []authzledger.ApprovalRuleCitation{{
			Category: "command", Pattern: "git push*", Effect: "ask",
			UnitID: "approvals/rules/release", PackID: "acme/policy", Scope: "project",
		}},
	}))
	testutil.FailErr(t, "commit", tx.Commit())
	rows, err := authzcontext.NewSQLStore(sqlDB).ListEvents(t.Context(), "sess-policy")
	testutil.FailErr(t, "list authz events", err)
	if len(rows) != 1 {
		t.Fatalf("events = %d want 1", len(rows))
	}
	var detail authzcontext.EventDetail
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &detail); err != nil {
		testutil.FailErr(t, "decode detail", err)
	}
	if len(detail.ApprovalRules) != 1 || detail.ApprovalRules[0].UnitID != "approvals/rules/release" || detail.ApprovalRules[0].Scope != "project" {
		t.Fatalf("approval rule provenance = %#v", detail.ApprovalRules)
	}
	if detail.ToolCallID != "call-policy" || detail.CheckpointID != "cp-1" || detail.PlanID != "plan-1" || detail.ActionDigest != "action-1" ||
		detail.SelectedOptionID != "deny" || len(detail.GrantIDs) != 1 || detail.GrantIDs[0] != "grant-1" ||
		detail.SubjectKind != "command" || detail.SubjectTitle != "git push" {
		t.Fatalf("approval decision provenance = %#v", detail)
	}
	if detail.Gate != "user_rule" || len(detail.Reasons) != 2 || detail.Reasons[0] != "user_rule" || detail.Reasons[1] != "sensitive_location" {
		t.Fatalf("approval decision gate = %q reasons = %v", detail.Gate, detail.Reasons)
	}
	if br := authzcontext.VerifyEvents("sess-policy", rows); br != nil {
		t.Fatalf("chain with gate and reasons does not verify: %+v", br)
	}
}

func TestLedger_chainAndRedaction(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	ledger := &authzcontext.Ledger{
		Store: mem,
		Audit: authzcontext.AuditConfig{CaptureRawArgv: false},
	}
	action := struct {
		tool string
		args map[string]any
	}{tool: "command", args: map[string]any{"command": "rm -rf /"}}
	detailIn := authzcontext.DetailInput{
		Tool:       action.tool,
		Args:       action.args,
		ProjectDir: t.TempDir(),
		RejectCode: approvaloutcome.CodeApprovalDenied,
	}
	ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
		SessionID:  "sess-chain",
		Action:     authzcontext.EventActionToolDenied,
		Outcome:    authzcontext.EventOutcomeDenied,
		ResolvedBy: authzcontext.ResolvedBySystemDeny,
		ToolName:   "command",
		RejectCode: approvaloutcome.CodeApprovalDenied,
		Detail:     detailIn,
	})
	rows, err := mem.ListEvents(context.Background(), "sess-chain")
	if err != nil || len(rows) != 1 {
		t.Fatalf("events = %d err = %v", len(rows), err)
	}
	var detail authzcontext.EventDetail
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &detail); err != nil {
		testutil.FailErr(t, "detail json", err)
	}
	if detail.RawArgv != "" {
		t.Fatalf("default redaction must omit raw_argv, got %q", detail.RawArgv)
	}
	if br := authzcontext.VerifyEvents("sess-chain", rows); br != nil {
		t.Fatalf("verify: %+v", br)
	}

	ledger.Audit.CaptureRawArgv = true
	ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
		SessionID:  "sess-chain",
		Action:     authzcontext.EventActionToolDenied,
		Outcome:    authzcontext.EventOutcomeDenied,
		ResolvedBy: authzcontext.ResolvedBySystemDeny,
		ToolName:   "command",
		Detail: authzcontext.DetailInput{
			Tool: action.tool,
			Args: action.args,
		},
	})
	rows, err = mem.ListEvents(context.Background(), "sess-chain")
	if err != nil || len(rows) != 2 {
		t.Fatalf("events = %d err = %v", len(rows), err)
	}
	if err := json.Unmarshal([]byte(rows[1].DetailJSON), &detail); err != nil {
		testutil.FailErr(t, "detail json", err)
	}
	if detail.RawArgv == "" {
		t.Fatal("capture_raw_argv=true should include raw_argv")
	}
}

func TestSQLStore_authzEventsChain(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	store := authzcontext.NewSQLStore(sqlDB)
	ledger := &authzcontext.Ledger{Store: store}
	insertSession(t, sqlDB, "sess-sql-ev", time.Now().UTC())
	if err := ledger.AppendGate(context.Background(), authzcontext.RecordInput{
		SessionID:  "sess-sql-ev",
		Action:     authzcontext.EventActionApprovalDecision,
		Outcome:    authzcontext.EventOutcomeAllowed,
		ResolvedBy: authzcontext.ResolvedByHuman,
		ToolName:   "write",
		Detail: authzcontext.DetailInput{
			Tool:       "write",
			GrantScope: authzcontext.GrantScopeOnce,
		},
	}); err != nil {
		testutil.FailErr(t, "append gate", err)
	}
	br, err := authzcontext.Verify(context.Background(), sqlDB)
	if err != nil || br != nil {
		t.Fatalf("verify br=%+v err=%v", br, err)
	}
}

func appendDecision(t *testing.T, sqlDB db.Handle, sessionID string) {
	t.Helper()
	ledger := &authzcontext.Ledger{Store: authzcontext.NewSQLStore(sqlDB)}
	ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
		SessionID:  sessionID,
		Action:     authzcontext.EventActionToolDenied,
		Outcome:    authzcontext.EventOutcomeDenied,
		ResolvedBy: authzcontext.ResolvedBySystemDeny,
		Detail:     authzcontext.DetailInput{Tool: "command"},
	})
}

func insertSession(t *testing.T, sqlDB db.Handle, id string, createdAt time.Time) {
	t.Helper()
	stamp := db.FormatTime(createdAt)
	_, err := sqlDB.ExecContext(t.Context(),
		`INSERT OR IGNORE INTO projects (id, last_opened_at, created_at) VALUES (?, ?, ?)`,
		"proj", stamp, stamp,
	)
	testutil.FailErr(t, "insert project", err)
	_, err = sqlDB.ExecContext(t.Context(),
		`INSERT INTO sessions (id, project_id, owner_person_id, posture, created_at, activity_at, updated_at) VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), ?, ?, ?, ?)`,
		id, "proj", "build", db.FormatTime(createdAt), db.FormatTime(createdAt), db.FormatTime(createdAt),
	)
	testutil.FailErr(t, "insert session", err)
}

func countEvents(t *testing.T, sqlDB db.Handle, sessionID string) int {
	t.Helper()
	var n int
	err := sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM authz_events WHERE session_id = ?`, sessionID).Scan(&n)
	testutil.FailErr(t, "count events", err)
	return n
}

func TestAuthorizationEventsRequireALiveSession(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := authzcontext.NewSQLStore(sqlDB)
	err := store.AppendEvent(context.Background(), authzcontext.Event{
		ID: "event-orphan", SessionID: "orphan-sess", EventSeq: 1, RecordedAt: time.Now().UTC(),
		Action: authzcontext.EventActionToolDenied, Outcome: authzcontext.EventOutcomeDenied,
		ResolvedBy: authzcontext.ResolvedBySystemDeny, HashVersion: authzcontext.HashVersion1,
	})
	if err == nil {
		t.Fatal("authorization event without a session was accepted")
	}
}

func TestSessionDeletionRemovesTheWholeChainAndVerifyStaysClean(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	for _, id := range []string{"kept", "removed"} {
		insertSession(t, sqlDB, id, time.Now().UTC())
		for range 3 {
			appendDecision(t, sqlDB, id)
		}
	}

	_, err := sqlDB.ExecContext(context.Background(), `DELETE FROM sessions WHERE id = ?`, "removed")
	testutil.FailErr(t, "delete session", err)

	if got := countEvents(t, sqlDB, "removed"); got != 0 {
		t.Errorf("deleted session left %d decision rows behind", got)
	}
	if got := countEvents(t, sqlDB, "kept"); got != 3 {
		t.Errorf("surviving session has %d rows, want its chain intact", got)
	}
	br, err := authzcontext.Verify(context.Background(), sqlDB)
	testutil.FailErr(t, "verify", err)
	if br != nil {
		t.Fatalf("verify reported a break after an ordinary deletion: %+v", br)
	}
}
