package store_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

type gb03V07SlotResult struct {
	Selector string   `json:"selector"`
	FamilyID string   `json:"family_id"`
	Lane     string   `json:"lane"`
	Passed   bool     `json:"passed"`
	Details  string   `json:"details,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
}

type storeCloseoutReader struct {
	st *store.Repositories
}

func (r storeCloseoutReader) LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error) {
	return r.st.Evidence.LoadLedger(ctx, sessionID)
}

func (r storeCloseoutReader) WorkerLegs(ctx context.Context, parentSessionID string, since time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

// TestGB03V07InProcessRestartAndUnreadableLedger executes all 4 GB03 V07
// in-process slots against D1's real SQLite evidence store, ledger loader,
// and guidance closeout reader via a test-only Go -overlay.
func TestGB03V07InProcessRestartAndUnreadableLedger(t *testing.T) {
	results := make([]gb03V07SlotResult, 0, 4)

	record := func(res gb03V07SlotResult) {
		results = append(results, res)
		if !res.Passed {
			t.Errorf("%s failed: %s (reasons=%v)", res.Selector, res.Details, res.Reasons)
		}
	}

	// ------------------------------------------------------------------
	// Slot 1: V07-03-inprocess-restart-before-commit-boundary
	// ------------------------------------------------------------------
	t.Run("V07-03-inprocess-restart-before-commit-boundary", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "v07-03.db")
		sqlDB1 := testdbfixture.OpenPath(t, dbPath)
		testdbseed.InsertProjectRoot(t, sqlDB1, testdbseed.DefaultProjectID, dir)
		st1 := store.NewSQL(sqlDB1)
		st1.SetDataDir(dir)

		ctx := context.Background()
		sess, err := st1.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}

		// Commit read#1 cleanly.
		h1, _, err := st1.Evidence.CommitEvidenceToolResult(
			ctx, sess.ID, dir, "read",
			map[string]any{"path": "a.go"},
			"1\tpackage demo\n2\tfunc Alpha() int { return 1 }\n",
		)
		if err != nil {
			t.Fatalf("commit read#1: %v", err)
		}

		// Abort/crash before commit of a second read via cancelled context.
		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()
		hAborted, _, abortErr := st1.Evidence.CommitEvidenceToolResult(
			canceledCtx, sess.ID, dir, "read",
			map[string]any{"path": "b.go"},
			"1\tpackage demo\n2\tfunc Beta() int { return 2 }\n",
		)

		// Restart: close DB1, open fresh DB2 + fresh store on same path.
		_ = sqlDB1.Close()
		sqlDB2 := testdbfixture.OpenPath(t, dbPath)
		defer sqlDB2.Close()
		st2 := store.NewSQL(sqlDB2)
		st2.SetDataDir(dir)

		evAfterCrash, loadErr := st2.Evidence.LoadLedger(ctx, sess.ID)
		h2, _, commit2Err := st2.Evidence.CommitEvidenceToolResult(
			ctx, sess.ID, dir, "read",
			map[string]any{"path": "b.go"},
			"1\tpackage demo\n2\tfunc Beta() int { return 2 }\n",
		)

		var reasons []string
		if h1 != "read#1" {
			reasons = append(reasons, "initial_handle_not_read1:"+h1)
		}
		if abortErr == nil {
			reasons = append(reasons, "aborted_commit_unexpectedly_succeeded")
		}
		if hAborted != "" {
			reasons = append(reasons, "aborted_commit_returned_handle:"+hAborted)
		}
		if loadErr != nil {
			reasons = append(reasons, "reload_after_crash_failed:"+loadErr.Error())
		}
		if len(evAfterCrash.Handles) != 1 {
			reasons = append(reasons, "phantom_handle_after_aborted_commit")
		}
		if _, ok := evAfterCrash.Handles["read#2"]; ok {
			reasons = append(reasons, "phantom_read2_present")
		}
		if commit2Err != nil {
			reasons = append(reasons, "post_restart_commit_failed:"+commit2Err.Error())
		}
		if h2 != "read#2" {
			reasons = append(reasons, "post_restart_handle_not_read2:"+h2)
		}

		record(gb03V07SlotResult{
			Selector: "V07-03-inprocess-restart-before-commit-boundary",
			FamilyID: "V07",
			Lane:     "in_process",
			Passed:   len(reasons) == 0,
			Details:  "h1=" + h1 + " h2=" + h2,
			Reasons:  reasons,
		})
	})

	// ------------------------------------------------------------------
	// Slot 2: V07-04-inprocess-restart-before-resolution-boundary
	// ------------------------------------------------------------------
	t.Run("V07-04-inprocess-restart-before-resolution-boundary", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "v07-04.db")
		sqlDB1 := testdbfixture.OpenPath(t, dbPath)
		testdbseed.InsertProjectRoot(t, sqlDB1, testdbseed.DefaultProjectID, dir)
		st1 := store.NewSQL(sqlDB1)
		st1.SetDataDir(dir)

		ctx := context.Background()
		sess, err := st1.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}

		body := "1\tpackage demo\n2\tfunc Alpha() int { return 42 }\n3\t// end\n"
		h1, _, err := st1.Evidence.CommitEvidenceToolResult(
			ctx, sess.ID, dir, "read",
			map[string]any{"path": "a.go"},
			body,
		)
		if err != nil {
			t.Fatalf("commit read#1: %v", err)
		}

		// Restart between commit and resolution.
		_ = sqlDB1.Close()
		sqlDB2 := testdbfixture.OpenPath(t, dbPath)
		defer sqlDB2.Close()
		st2 := store.NewSQL(sqlDB2)
		st2.SetDataDir(dir)

		ev, loadErr := st2.Evidence.LoadLedger(ctx, sess.ID)
		roots := evidence.CitationRoots{ProjectDir: dir}
		res := evidence.Resolve(
			roots,
			evidence.Triple{Path: "a.go", Line: 2, Excerpt: "func Alpha() int { return 42 }"},
			ev,
			"read#1",
		)

		// Verify next commit after restart mints read#2 without handle reuse.
		h2, _, commit2Err := st2.Evidence.CommitEvidenceToolResult(
			ctx, sess.ID, dir, "read",
			map[string]any{"path": "b.go"},
			"1\tpackage demo\n2\tfunc Beta() int { return 7 }\n",
		)

		var reasons []string
		if h1 != "read#1" {
			reasons = append(reasons, "h1_not_read1:"+h1)
		}
		if loadErr != nil {
			reasons = append(reasons, "load_ledger_failed:"+loadErr.Error())
		}
		if res.Verdict != evidence.VerdictMatched {
			reasons = append(reasons, "resolve_verdict_not_matched:"+string(res.Verdict))
		}
		if res.Handle != "read#1" {
			reasons = append(reasons, "resolve_handle_not_read1:"+res.Handle)
		}
		if commit2Err != nil {
			reasons = append(reasons, "post_restart_commit_failed:"+commit2Err.Error())
		}
		if h2 != "read#2" {
			reasons = append(reasons, "post_restart_handle_reuse:"+h2)
		}

		record(gb03V07SlotResult{
			Selector: "V07-04-inprocess-restart-before-resolution-boundary",
			FamilyID: "V07",
			Lane:     "in_process",
			Passed:   len(reasons) == 0,
			Details:  "verdict=" + string(res.Verdict) + " handle=" + res.Handle + " next=" + h2,
			Reasons:  reasons,
		})
	})

	// ------------------------------------------------------------------
	// Slot 3: V07-05-inprocess-unreadable-ledger-refuses-all-citations
	// ------------------------------------------------------------------
	t.Run("V07-05-inprocess-unreadable-ledger-refuses-all-citations", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "v07-05.db")
		sqlDB := testdbfixture.OpenPath(t, dbPath)
		defer sqlDB.Close()
		testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
		st := store.NewSQL(sqlDB)
		st.SetDataDir(dir)

		ctx := context.Background()
		sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}

		if _, _, err := st.Evidence.CommitEvidenceToolResult(
			ctx, sess.ID, dir, "read",
			map[string]any{"path": "a.go"},
			"1\tpackage demo\n2\tfunc Alpha() int { return 42 }\n",
		); err != nil {
			t.Fatalf("commit read#1: %v", err)
		}
		if err := st.Evidence.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
			Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL,
			SourceTool: "fetch_url", URL: "https://docs.example.test/spec",
		}); err != nil {
			t.Fatalf("upsert web#1: %v", err)
		}

		// Corrupt the content blob for read#1 so LoadLedger fails.
		var sha string
		if err := sqlDB.QueryRowContext(
			ctx,
			`SELECT content_blob_sha256 FROM evidence_records WHERE session_id = ? AND handle = 'read#1'`,
			sess.ID,
		).Scan(&sha); err != nil {
			t.Fatalf("query blob sha: %v", err)
		}
		rel, err := contentblob.RelPath(sha)
		if err != nil {
			t.Fatalf("rel path: %v", err)
		}
		blobPath := filepath.Join(contentblob.StoreFor(dir, testdbseed.DefaultProjectID).Root, filepath.FromSlash(rel))
		if err := os.WriteFile(blobPath, []byte("not-valid-zstd-corrupt-ledger"), 0o600); err != nil {
			t.Fatalf("corrupt blob: %v", err)
		}

		ev, loadErr := st.Evidence.LoadLedger(ctx, sess.ID)
		reader := storeCloseoutReader{st: st}
		_, unionErr := guidance.UnionCloseoutEvidence(ctx, reader, sess.ID, nil)

		roots := evidence.CitationRoots{ProjectDir: dir}
		probes := []struct {
			name   string
			triple evidence.Triple
			handle string
		}{
			{"handle_only", evidence.Triple{}, "read#1"},
			{"path_coord", evidence.Triple{Path: "a.go", Line: 2}, ""},
			{"handle_path", evidence.Triple{Path: "a.go", Line: 2, Excerpt: "func Alpha()"}, "read#1"},
			{"url_handle", evidence.Triple{Path: "https://docs.example.test/spec"}, "web#1"},
			{"artifact_handle", evidence.Triple{Path: "art-1"}, "render#1"},
		}

		var reasons []string
		if loadErr == nil {
			reasons = append(reasons, "corrupt_ledger_loaded_without_error")
		}
		if unionErr == nil {
			reasons = append(reasons, "union_closeout_evidence_swallowed_error")
		}
		if len(ev.Handles) != 0 {
			reasons = append(reasons, "unreadable_ledger_returned_nonempty_handles")
		}
		for _, h := range []string{"read#1", "web#1", "render#1"} {
			if _, ok := evidence.ResolveHandle(ev, h); ok {
				reasons = append(reasons, "handle_resolved_despite_unreadable_ledger:"+h)
			}
		}
		for _, p := range probes {
			res := evidence.Resolve(roots, p.triple, ev, p.handle)
			if res.Verdict.Grounded() {
				reasons = append(reasons, "citation_grounded_despite_unreadable_ledger:"+p.name)
			}
		}

		record(gb03V07SlotResult{
			Selector: "V07-05-inprocess-unreadable-ledger-refuses-all-citations",
			FamilyID: "V07",
			Lane:     "in_process",
			Passed:   len(reasons) == 0,
			Details:  "loadErr!=nil unionErr!=nil probes=5",
			Reasons:  reasons,
		})
	})

	// ------------------------------------------------------------------
	// Slot 4: V07-06-inprocess-unreadable-ledger-denies-exemption
	// ------------------------------------------------------------------
	t.Run("V07-06-inprocess-unreadable-ledger-denies-exemption", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "v07-06.db")
		sqlDB := testdbfixture.OpenPath(t, dbPath)
		testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
		st := store.NewSQL(sqlDB)
		st.SetDataDir(dir)

		ctx := context.Background()
		sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, _, err := st.Evidence.CommitEvidenceToolResult(
			ctx, sess.ID, dir, "read",
			map[string]any{"path": "a.go"},
			"1\tpackage demo\n",
		); err != nil {
			t.Fatalf("commit read#1: %v", err)
		}

		// Close the underlying DB to make the entire ledger unreadable.
		_ = sqlDB.Close()

		reader := storeCloseoutReader{st: st}
		midEv, hasCitable, midErr := guidance.MidTurnEvidence(ctx, reader, sess.ID, nil)
		unionEv, unionErr := guidance.UnionCloseoutEvidence(ctx, reader, sess.ID, nil)

		var reasons []string
		if midErr == nil {
			reasons = append(reasons, "mid_turn_evidence_treated_unreadable_as_empty")
		}
		if hasCitable {
			reasons = append(reasons, "mid_turn_evidence_reported_citable_on_error")
		}
		if len(midEv.Handles) != 0 {
			reasons = append(reasons, "mid_turn_evidence_invented_handles")
		}
		if unionErr == nil {
			reasons = append(reasons, "union_closeout_evidence_treated_unreadable_as_empty")
		}
		if len(unionEv.Handles) != 0 {
			reasons = append(reasons, "union_closeout_evidence_invented_handles")
		}

		record(gb03V07SlotResult{
			Selector: "V07-06-inprocess-unreadable-ledger-denies-exemption",
			FamilyID: "V07",
			Lane:     "in_process",
			Passed:   len(reasons) == 0,
			Details:  "midErr!=nil unionErr!=nil handles=0",
			Reasons:  reasons,
		})
	})

	if resultsPath := strings.TrimSpace(os.Getenv("GND_GB03_V07_RESULTS_JSONL")); resultsPath != "" {
		var sb strings.Builder
		for _, r := range results {
			enc, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			sb.Write(enc)
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(resultsPath, []byte(sb.String()), 0o600); err != nil {
			t.Fatalf("write results: %v", err)
		}
	}
}
