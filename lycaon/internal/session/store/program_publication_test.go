package store

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramPublicationRecovery(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "publication.db")
	database := testdbfixture.OpenPath(t, path)
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)
	chat, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create chat", err)
	child, err := st.Sessions.CreateChild(ctx, chat, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	const programID = "first-program"
	seedPublicationProgram(t, st, child.ID, programID)
	target := evidence.ProgramProvenanceTarget{SessionID: child.ID, ChatSessionID: chat.ID, ProjectDir: child.WorkspacePath}
	const url = "https://example.invalid/custody"
	prov := evidence.ProgramHTTPProvenance{ProgramID: programID, ObservedURLs: []string{url},
		UntrustedContent: true, CustodyPaths: []string{"report.txt"}}

	if err := st.Evidence.BeginProgramPublication(ctx, chat.ID, programID); err == nil {
		t.Fatal("another session began the child program publication")
	}
	testutil.FailErr(t, "begin publication", st.Evidence.BeginProgramPublication(ctx, child.ID, programID))
	if err := st.Evidence.CommitProgramPublication(ctx, child.ID, programID); err == nil {
		t.Fatal("commit accepted a preparing publication")
	}
	if err := st.Evidence.MarkProgramPublicationMaterialized(ctx, child.ID, programID); err == nil {
		t.Fatal("materialization accepted a preparing publication")
	}
	testutil.FailErr(t, "stage intent", st.Evidence.StageProgramPublication(ctx, target, prov))
	testutil.FailErr(t, "stage identical intent", st.Evidence.StageProgramPublication(ctx, target, prov))
	changed := prov
	changed.CustodyPaths = []string{"different.txt"}
	if err := st.Evidence.StageProgramPublication(ctx, target, changed); err == nil {
		t.Fatal("staging replaced existing intent")
	}

	testutil.FailErr(t, "close store after staging", database.Close())
	database = testdbfixture.OpenPath(t, path)
	st = NewSQL(database)
	key := evidence.ProgramPublicationKey{SessionID: child.ID, ProgramID: programID}
	assertRecoverablePublications(t, st, "", key)

	_, err = st.DB().ExecContext(ctx, `CREATE TRIGGER reject_chat_publication BEFORE INSERT ON evidence_records
		WHEN NEW.session_id IN (SELECT id FROM sessions WHERE parent_session_id IS NULL)
		BEGIN SELECT RAISE(ABORT, 'chat custody rejected'); END`)
	testutil.FailErr(t, "inject chat evidence failure", err)
	if err := st.Evidence.CommitProgramPublication(ctx, child.ID, programID); err == nil {
		t.Fatal("chat write failure did not abort the publication")
	}
	assertRecoverablePublications(t, st, "", key)
	if err := st.Evidence.MarkProgramPublicationMaterialized(ctx, child.ID, programID); err == nil {
		t.Fatal("failed settlement left the publication committed")
	}
	for _, sessionID := range []string{chat.ID, child.ID} {
		ledger, err := st.Evidence.LoadLedger(ctx, sessionID)
		testutil.FailErr(t, "read rolled-back ledger", err)
		if len(ledger.Handles) != 0 || st.Evidence.SessionUntrustedContent(sessionID) {
			t.Fatalf("failed settlement changed evidence for %s", sessionID)
		}
	}

	_, err = st.DB().ExecContext(ctx, `DROP TRIGGER reject_chat_publication`)
	testutil.FailErr(t, "remove evidence failure", err)
	testutil.FailErr(t, "commit exact staged intent", st.Evidence.CommitProgramPublication(ctx, child.ID, programID))
	assertRecoverablePublications(t, st, "", key)
	testutil.FailErr(t, "close store after settlement", database.Close())
	st = NewSQL(testdbfixture.OpenPath(t, path))
	testutil.FailErr(t, "repeat publication begin", st.Evidence.BeginProgramPublication(ctx, child.ID, programID))
	testutil.FailErr(t, "repeat publication stage", st.Evidence.StageProgramPublication(ctx, target, prov))
	testutil.FailErr(t, "repeat publication commit", st.Evidence.CommitProgramPublication(ctx, child.ID, programID))
	for _, sessionID := range []string{chat.ID, child.ID} {
		ledger, err := st.Evidence.LoadLedger(ctx, sessionID)
		testutil.FailErr(t, "read settled ledger", err)
		if len(ledger.Handles) != 1 {
			t.Fatalf("settlement should create one record in %s, got %d", sessionID, len(ledger.Handles))
		}
		urls, err := st.Evidence.ExternalCustodyURLsForPaths(ctx, sessionID, target.ProjectDir, []string{"report.txt"})
		testutil.FailErr(t, "read committed custody", err)
		if !slices.Equal(urls, []string{url}) {
			t.Fatalf("custody for %s = %v", sessionID, urls)
		}
	}
	if !st.Evidence.SessionUntrustedContent(child.ID) || st.Evidence.SessionUntrustedContent(chat.ID) {
		t.Fatal("settlement should mark only the consuming session untrusted")
	}
	testutil.FailErr(t, "acknowledge output materialization", st.Evidence.MarkProgramPublicationMaterialized(ctx, child.ID, programID))
	testutil.FailErr(t, "repeat materialization acknowledgment", st.Evidence.MarkProgramPublicationMaterialized(ctx, child.ID, programID))
	assertRecoverablePublications(t, st, "")
}

func TestProgramPublicationRecoveryPagesAndIntentLimits(t *testing.T) {
	ctx := t.Context()
	st := newSQLTestStore(t)
	sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	target := evidence.ProgramProvenanceTarget{SessionID: sess.ID, ProjectDir: sess.WorkspacePath}
	for _, programID := range []string{"a-committed", "b-staged", "c-preparing"} {
		seedPublicationProgram(t, st, sess.ID, programID)
		testutil.FailErr(t, "begin publication", st.Evidence.BeginProgramPublication(ctx, sess.ID, programID))
		if programID != "c-preparing" {
			testutil.FailErr(t, "stage intent with no provenance", st.Evidence.StageProgramPublication(ctx, target, evidence.ProgramHTTPProvenance{ProgramID: programID}))
		}
	}
	testutil.FailErr(t, "commit intent with no provenance", st.Evidence.CommitProgramPublication(ctx, sess.ID, "a-committed"))
	first := evidence.ProgramPublicationKey{SessionID: sess.ID, ProgramID: "a-committed"}
	second := evidence.ProgramPublicationKey{SessionID: sess.ID, ProgramID: "b-staged"}
	keys, err := st.Evidence.ListRecoverableProgramPublications(ctx, "", 1)
	testutil.FailErr(t, "list first recovery item", err)
	if !slices.Equal(keys, []evidence.ProgramPublicationKey{first}) {
		t.Fatalf("first publication page = %v", keys)
	}
	assertRecoverablePublications(t, st, first.ProgramID, second)
	assertRecoverablePublications(t, st, second.ProgramID)
	if _, err := st.Evidence.ListRecoverableProgramPublications(ctx, "", 0); err == nil {
		t.Fatal("invalid recovery page was accepted")
	}
	for _, value := range []string{strings.Repeat("x", maxProgramPublicationIntentBytes), strings.Repeat("\"", maxProgramPublicationIntentBytes/2), "\xff"} {
		prov := evidence.ProgramHTTPProvenance{ProgramID: "c-preparing", CustodyPaths: []string{value}}
		if err := st.Evidence.StageProgramPublication(ctx, target, prov); err == nil {
			t.Fatal("oversized or non-UTF-8 publication intent was accepted")
		}
	}
	assertRecoverablePublications(t, st, "", first, second)
}

func TestDelayedProgramPublicationPreservesCustodyOrder(t *testing.T) {
	ctx := t.Context()
	st := newSQLTestStore(t)
	chat, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create chat", err)
	child, err := st.Sessions.CreateChild(ctx, chat, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	target := evidence.ProgramProvenanceTarget{SessionID: child.ID, ChatSessionID: chat.ID, ProjectDir: child.WorkspacePath}

	for _, input := range []struct{ id, url string }{
		{"z-older", "https://example.invalid/older"},
		{"a-newer", "https://example.invalid/newer"},
	} {
		seedPublicationProgram(t, st, child.ID, input.id)
		testutil.FailErr(t, "begin publication", st.Evidence.BeginProgramPublication(ctx, child.ID, input.id))
		testutil.FailErr(t, "stage custody paths", st.Evidence.StageProgramPublication(ctx, target, evidence.ProgramHTTPProvenance{
			ProgramID: input.id, ObservedURLs: []string{input.url}, CustodyPaths: []string{"shared.txt", "replaced.txt"},
		}))
	}
	_, _, err = st.Evidence.CommitEvidenceToolResult(ctx, child.ID, target.ProjectDir, "write",
		map[string]any{"path": "replaced.txt"}, "wrote replaced.txt")
	testutil.FailErr(t, "record child replacement ahead of delayed publications", err)
	_, err = st.Evidence.CommitProgramHTTPProvenance(ctx, evidence.ProgramProvenanceTarget{SessionID: chat.ID, ProjectDir: target.ProjectDir},
		evidence.ProgramHTTPProvenance{ClearedPaths: []string{"replaced.txt"}})
	testutil.FailErr(t, "record chat replacement ahead of delayed publications", err)

	testutil.FailErr(t, "commit newer staged publication first", st.Evidence.CommitProgramPublication(ctx, child.ID, "a-newer"))
	testutil.FailErr(t, "commit older staged publication last", st.Evidence.CommitProgramPublication(ctx, child.ID, "z-older"))
	for _, sessionID := range []string{chat.ID, child.ID} {
		urls, err := st.Evidence.ExternalCustodyURLsForPaths(ctx, sessionID, target.ProjectDir, []string{"shared.txt"})
		testutil.FailErr(t, "read final staged custody", err)
		if !slices.Equal(urls, []string{"https://example.invalid/newer"}) {
			t.Fatalf("staged custody order for %s = %v", sessionID, urls)
		}
		urls, err = st.Evidence.ExternalCustodyURLsForPaths(ctx, sessionID, target.ProjectDir, []string{"replaced.txt"})
		testutil.FailErr(t, "read final replacement custody", err)
		if len(urls) != 0 {
			t.Fatalf("delayed publication replaced later local custody for %s: %v", sessionID, urls)
		}
	}
}

func seedPublicationProgram(t *testing.T, st *Repositories, sessionID, programID string) {
	t.Helper()
	receiptID := "receipt-" + programID
	_, err := st.DB().ExecContext(t.Context(), `INSERT INTO invocation_receipts
		(id, project_id, session_id, tool_call_id, tool_name, contract_digest, args_digest, owner, lifecycle,
		reversibility, evidence_policy, recovery_policy, status, started_at)
		VALUES (?, ?, ?, ?, 'command', '', '', 'command', 'effect_attempt', 'irreversible', 'result', 'abandon', 'running', '')`,
		receiptID, testdbseed.DefaultProjectID, sessionID, programID)
	testutil.FailErr(t, "seed invocation receipt", err)
	_, err = st.DB().ExecContext(t.Context(), `INSERT INTO invocation_programs
		(id, parent_receipt_id, root_execution_id, language_version, catalog_digest, source_unit_id, source_digest,
		source_ref, source_exact, plan_ref, syntax_validated, state, started_at)
		VALUES (?, ?, ?, 1, '', '', '', '', 0, '', 0, 'running', '')`,
		programID, receiptID, "root-"+programID)
	testutil.FailErr(t, "seed program receipt", err)
}

func assertRecoverablePublications(t *testing.T, st *Repositories, after string, want ...evidence.ProgramPublicationKey) {
	t.Helper()
	got, err := st.Evidence.ListRecoverableProgramPublications(t.Context(), after, 64)
	testutil.FailErr(t, "list recoverable publications", err)
	if !slices.Equal(got, want) {
		t.Fatalf("recoverable publications after %q = %v, want %v", after, got, want)
	}
}
