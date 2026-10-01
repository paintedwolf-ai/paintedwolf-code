package session

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type storeLedgerReader struct {
	store Store
}

func (r storeLedgerReader) LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error) {
	return r.store.LoadLedger(ctx, sessionID)
}

type evidenceRowSnapshot struct {
	Handle            string
	Ordinal           int
	Kind              string
	Shape             string
	TrustTier         string
	SourceTool        string
	Path              string
	URL               string
	LineRanges        string
	ContentBlobSha256 string
	Truncated         int
	Survey            int
	SupersededBy      sql.NullString
}

func queryEvidenceRows(t *testing.T, database db.Handle, sessionID string) []evidenceRowSnapshot {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `
		SELECT handle, ordinal, kind, shape, fidelity, source_tool, path, url, line_ranges, content_blob_sha256, truncated, survey, superseded_by
		FROM evidence_records
		WHERE session_id = ?
		ORDER BY handle ASC
	`, sessionID)
	testutil.FailErr(t, "query evidence_records", err)
	t.Cleanup(func() { _ = rows.Close() })
	var out []evidenceRowSnapshot
	for rows.Next() {
		var snap evidenceRowSnapshot
		testutil.FailErr(t, "scan evidence row", rows.Scan(
			&snap.Handle, &snap.Ordinal, &snap.Kind, &snap.Shape, &snap.TrustTier, &snap.SourceTool,
			&snap.Path, &snap.URL, &snap.LineRanges, &snap.ContentBlobSha256, &snap.Truncated, &snap.Survey, &snap.SupersededBy,
		))
		out = append(out, snap)
	}
	testutil.FailErr(t, "evidence rows iteration", rows.Err())
	return out
}

type groundingAuditSnapshot struct {
	WorkerSummaryStatus string
	WorkerCitationCode  string
	InvestigateCode     string
	SynthesisCode       string
	UnverifiableCode    string
}

func runGroundingAudits(t *testing.T, ctx context.Context, store Store, sessionID, projectDir string) groundingAuditSnapshot {
	t.Helper()
	ledger := storeLedgerReader{store: store}
	ev, err := store.LoadLedger(ctx, sessionID)
	testutil.FailErr(t, "LoadLedger", err)

	workerEval, workerEvalErr := workercompletion.EvaluateWorkerSummary(ctx, workercompletion.WorkerSummaryEvalInput{
		AgentType:      orchestration.ProfileCodeReviewer,
		ChildSessionID: sessionID,
		ProjectDir:     projectDir,
		Ledger:         ledger,
		Report: workercompletion.WorkerCompletionReport{
			Brief: "Reviewed src/a.go",
			Findings: []workercompletion.WorkerFinding{{
				Path: "src/a.go", Line: 1, Excerpt: "package a",
			}},
		},
	})
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	workerCitation := guidance.EvaluateWorkerCitations(
		evidence.CitationRoots{ProjectDir: projectDir},
		[]guidance.WorkerFindingInput{{Path: "src/a.go", Line: 1, Excerpt: "package a"}},
		nil,
		guidance.WorkerNarrativeInput{},
		ev,
	)
	coordReport := guidance.CoordinatorCompletionReport{
		Synthesis: "Finding in src/a.go",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "package a",
		}},
	}
	investigate := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: projectDir}, "implement_investigate", coordReport, guidance.CloseoutEvidence{Ledger: ev})
	synth := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: projectDir}, "implement_synthesis", coordReport, guidance.CloseoutEvidence{
		Ledger: evidence.NamespaceLedger(ev, "leg-a"),
	})
	unverifiable := guidance.EvaluateWorkerCitations(
		evidence.CitationRoots{ProjectDir: projectDir},
		[]guidance.WorkerFindingInput{{Path: "internal/phantom.go", Line: 1, Excerpt: "fabricated"}},
		nil,
		guidance.WorkerNarrativeInput{},
		ev,
	)
	return groundingAuditSnapshot{
		WorkerSummaryStatus: workerEval.Status,
		WorkerCitationCode:  workerCitation.Code,
		InvestigateCode:     investigate.Code,
		SynthesisCode:       synth.Code,
		UnverifiableCode:    unverifiable.Code,
	}
}

func newSQLCompactionManager(t *testing.T, store Store, cfg compaction.CompactionConfig) *Manager {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	mgr := NewManager(store, llm.NewMockProvider(testMockConfig(t)), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetCompactor(compaction.NewSimpleCompactor(cfg, compaction.MockSummarizer{Text: "Continue from compacted context."}))
	return mgr
}

// TestEvidenceLedgerCompactionIndependence proves compaction leaves evidence_records
// byte-identical and grounding audits stable.
func TestEvidenceLedgerCompactionIndependence(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := store.NewSQL(sqlDB)
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ChunkTokenThreshold = 500
	cfg.ChunkMinSavingsTokens = 100
	mgr := newSQLCompactionManager(t, store, cfg)

	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	srcPath := filepath.Join(projectDir, "src", "a.go")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(srcPath, []byte("package a\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	readJSON := `{"path":"src/a.go","content":"1|package a","offset":1,"end_line":1,"limit":1}`
	handle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, projectDir, "read", map[string]any{"path": "src/a.go"}, readJSON)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)
	if handle != "read#1" {
		t.Fatalf("handle = %q want read#1", handle)
	}

	beforeRows := queryEvidenceRows(t, sqlDB, sess.ID)
	beforeAudits := runGroundingAudits(t, ctx, store, sess.ID, projectDir)

	huge := strings.Repeat("ERROR: build failed\n", 8000)
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: "survey"},
		api.Message{ID: "t-big", Role: api.MessageRoleTool, Content: huge},
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, Content: "seen"},
	); err != nil {
		t.Fatal(err)
	}

	if err := mgr.compactOversizedToolResultsInSession(ctx, sess); err != nil {
		testutil.FailErr(t, "compactOversizedToolResultsInSession", err)
	}
	mgr.waitForCompaction()
	// Messages stay canonical; the compacted chunk lives only in the applied view.
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, m := range msgs {
		if m.ID == "t-big" && m.Content != huge {
			t.Fatal("canonical tool message was mutated by compaction")
		}
	}
	view := appliedView(t, mgr, store, sess.ID)
	compacted := false
	for _, m := range view {
		if m.CompactedChunk != nil {
			compacted = true
			break
		}
	}
	if !compacted {
		t.Fatal("expected at least one compacted tool chunk in the view")
	}

	afterRows := queryEvidenceRows(t, sqlDB, sess.ID)
	if fmt.Sprint(beforeRows) != fmt.Sprint(afterRows) {
		t.Fatalf("evidence_records changed after compaction\nbefore: %+v\nafter:  %+v", beforeRows, afterRows)
	}

	afterAudits := runGroundingAudits(t, ctx, store, sess.ID, projectDir)
	if beforeAudits != afterAudits {
		t.Fatalf("audit outcomes changed after compaction\nbefore: %+v\nafter:  %+v", beforeAudits, afterAudits)
	}
	if afterAudits.WorkerSummaryStatus != "complete" {
		t.Fatalf("worker summary status = %q want complete", afterAudits.WorkerSummaryStatus)
	}
	if afterAudits.UnverifiableCode != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("unverifiable code = %q want %q", afterAudits.UnverifiableCode, guidance.WorkerEvidenceHandleUnknownCode)
	}
}
