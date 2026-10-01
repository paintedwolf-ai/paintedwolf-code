package search

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// seedStoreRows indexes fixtures under one session.
func seedStoreRows(t *testing.T, sessionID string, rows []IndexRow) db.Handle {
	t.Helper()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	ctx := context.Background()
	testdbseed.InsertSessionWithRoot(t, sqlDB, sessionID, testdbseed.DefaultProjectID, dir)
	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	for i := range rows {
		rows[i].ProjectID = testdbseed.DefaultProjectID
		rows[i].SessionID = sessionID
		if rows[i].TS == "" {
			rows[i].TS = time.Now().UTC().Format(time.RFC3339)
		}
	}
	testutil.FailErr(t, "upsert rows", NewStore().UpsertRows(ctx, tx, rows))
	testutil.FailErr(t, "Commit", tx.Commit())
	return sqlDB
}

func runStoreQuery(t *testing.T, sqlDB db.Handle, query string, flags MatchFlags) []Hit {
	t.Helper()
	plan, err := CompileQuery(query, CompileContext{
		OriginProjectID: testdbseed.DefaultProjectID,
		Flags:           flags,
	})
	testutil.FailErr(t, "CompileQuery", err)
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	report, err := NewStoreExecutor(sqlDB).Run(context.Background(), PlanLeg{
		Executor: ExecutorStore, Cap: SearchExecutorProbeHits, Store: plan.Store,
	})
	testutil.FailErr(t, "StoreExecutor.Run", err)
	return report.Hits
}

func storeSnippets(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Snippet)
	}
	return out
}

func containsSnippet(hits []Hit, snippet string) bool {
	for _, hit := range hits {
		if hit.Snippet == snippet {
			return true
		}
	}
	return false
}

func TestStoreFlagsRecheckFullMessageContent(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	const sessionID = "sess-long-message"
	const messageID = "message-long"
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertSessionWithRoot(t, sqlDB, sessionID, projectID, dir)
	content := strings.Repeat("preview ", 100) + "NeedleAfterPreview"
	ts := time.Now().UTC().Format(time.RFC3339)
	testdbseed.InsertSessionEntry(t, sqlDB, "entry-"+messageID, sessionID, "model_output", messageID, 1)
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO messages
		(id, entry_id, session_id, role, content, origin, authority, trust_tier, ts)
		VALUES (?, ?, ?, 'assistant', ?, 'model', 'none', 'trusted', ?)`, messageID, "entry-"+messageID, sessionID, content, ts)
	testutil.FailErr(t, "insert message", err)
	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin evidence projection", err)
	testutil.FailErr(t, "upsert message projection", NewStore().UpsertRows(context.Background(), tx, []IndexRow{{
		ID:        "message-row",
		ProjectID: projectID,
		Source:    SourceMessage,
		HitKind:   HitKindMessage,
		SessionID: sessionID,
		MessageID: messageID,
		SourceRef: messageID,
		Snippet:   content[:messageSnippetMaxRunes],
		TS:        ts,
	}}))
	testutil.FailErr(t, "commit evidence projection", tx.Commit())

	tests := []struct {
		name  string
		query string
		flags MatchFlags
	}{
		{name: "case-sensitive", query: "NeedleAfterPreview", flags: MatchFlags{CaseSensitive: true}},
		{name: "whole-word", query: "NeedleAfterPreview", flags: MatchFlags{WholeWord: true}},
		{name: "regex", query: `"NeedleAfterPreview$"`, flags: MatchFlags{Regex: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits := runStoreQuery(t, sqlDB, tc.query, tc.flags)
			if len(hits) != 1 || hits[0].SourceRef != messageID {
				t.Fatalf("hits = %+v, want the full-message match", hits)
			}
		})
	}
}

func TestStoreRejectsMissingHitKind(t *testing.T) {
	err := NewStore().UpsertRows(context.Background(), nil, []IndexRow{{}})
	if err == nil || !strings.Contains(err.Error(), "hit_kind is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreExecutorBM25RanksStrongerSnippetFirst(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	ctx := context.Background()
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-bm25", projectID, dir)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	store := NewStore()
	tsStrong := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	tsWeak := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	testutil.FailErr(t, "upsert strong", store.UpsertRows(ctx, tx, []IndexRow{{
		ID:        "ev-strong",
		ProjectID: projectID,
		Source:    SourceTool,
		HitKind:   HitKindEvidence,
		SessionID: "sess-bm25",
		Snippet:   "wal checkpoint wal checkpoint wal checkpoint",
		Path:      "docs/wal.md",
		TS:        tsStrong,
	}}))
	testutil.FailErr(t, "upsert weak", store.UpsertRows(ctx, tx, []IndexRow{{
		ID:        "ev-weak",
		ProjectID: projectID,
		Source:    SourceTool,
		HitKind:   HitKindEvidence,
		SessionID: "sess-bm25",
		Snippet:   "notes about wal checkpoint briefly",
		Path:      "docs/other.md",
		TS:        tsWeak,
	}}))
	testutil.FailErr(t, "Commit", tx.Commit())

	plan, err := CompileQuery("wal checkpoint", CompileContext{
		OriginProjectID: projectID,
		RootsForProject: func(string) ([]CodeRoot, error) { return nil, nil },
		AttachedProjectIDs: func() ([]string, error) {
			return []string{projectID}, nil
		},
	})
	testutil.FailErr(t, "CompileQuery", err)
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}

	report, err := NewStoreExecutor(sqlDB).Run(ctx, PlanLeg{
		Executor: ExecutorStore,
		Cap:      SearchExecutorProbeHits,
		Store:    plan.Store,
	})
	testutil.FailErr(t, "StoreExecutor.Run", err)
	hits := report.Hits
	if len(hits) < 2 {
		t.Fatalf("hits = %d, want ≥2; args=%v", len(hits), plan.Store.Args)
	}
	if hits[0].Snippet != "wal checkpoint wal checkpoint wal checkpoint" {
		t.Fatalf("first snippet = %q, want strong wal checkpoint match", hits[0].Snippet)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("scores = %v vs %v, want strong > weak", hits[0].Score, hits[1].Score)
	}
	if hits[0].Score <= 0 {
		t.Fatalf("strong score = %v, want positive negated bm25", hits[0].Score)
	}
	if hits[0].ID == "" || hits[0].ID == hits[1].ID {
		t.Fatalf("store hit ids = %q, %q; want stable unique ids", hits[0].ID, hits[1].ID)
	}
}

func TestStoreExecutorPreservesBooleanOr(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	ctx := context.Background()
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-or", projectID, dir)
	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	store := NewStore()
	testutil.FailErr(t, "upsert OR rows", store.UpsertRows(ctx, tx, []IndexRow{
		{
			ID: "ev-alpha", ProjectID: projectID, Source: SourceTool, HitKind: HitKindEvidence,
			SessionID: "sess-or", Snippet: "alpha only", TS: time.Now().UTC().Format(time.RFC3339),
		},
		{
			ID: "ev-beta", ProjectID: projectID, Source: SourceTool, HitKind: HitKindEvidence,
			SessionID: "sess-or", Snippet: "beta only", TS: time.Now().UTC().Format(time.RFC3339),
		},
	}))
	testutil.FailErr(t, "Commit", tx.Commit())

	plan, err := CompileQuery("alpha OR beta", CompileContext{OriginProjectID: projectID})
	testutil.FailErr(t, "CompileQuery", err)
	report, err := NewStoreExecutor(sqlDB).Run(ctx, PlanLeg{
		Executor: ExecutorStore, Cap: SearchExecutorProbeHits, Store: plan.Store,
	})
	testutil.FailErr(t, "StoreExecutor.Run", err)
	hits := report.Hits
	if len(hits) != 2 {
		t.Fatalf("OR hits = %+v, want alpha-only and beta-only rows; args=%v", hits, plan.Store.Args)
	}
}

func TestStoreExecutorPreservesMixedTextFilterOr(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	ctx := context.Background()
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-mixed-or", projectID, dir)
	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	store := NewStore()
	testutil.FailErr(t, "upsert mixed OR rows", store.UpsertRows(ctx, tx, []IndexRow{
		{
			ID: "ev-alpha", ProjectID: projectID, Source: SourceTool, HitKind: HitKindEvidence,
			SessionID: "sess-mixed-or", Snippet: "alpha only", TS: time.Now().UTC().Format(time.RFC3339),
		},
		{
			ID: "ev-web", ProjectID: projectID, Source: SourceTool, HitKind: HitKindWeb,
			SessionID: "sess-mixed-or", Snippet: "unrelated web result", TS: time.Now().UTC().Format(time.RFC3339),
		},
	}))
	testutil.FailErr(t, "Commit", tx.Commit())

	plan, err := CompileQuery("alpha OR kind:web", CompileContext{OriginProjectID: projectID})
	testutil.FailErr(t, "CompileQuery", err)
	report, err := NewStoreExecutor(sqlDB).Run(ctx, PlanLeg{
		Executor: ExecutorStore, Cap: SearchExecutorProbeHits, Store: plan.Store,
	})
	testutil.FailErr(t, "StoreExecutor.Run", err)
	hits := report.Hits
	if len(hits) != 2 {
		t.Fatalf("mixed OR hits = %+v, want text arm and filter arm; args=%v", hits, plan.Store.Args)
	}
}

func TestStoreExecutorCaseSensitiveFiltersFreeText(t *testing.T) {
	sqlDB := seedStoreRows(t, "sess-case", []IndexRow{
		{ID: "ev-exact", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "Deploy Toolbar ready"},
		{ID: "ev-lower", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "deploy toolbar ready"},
	})
	loose := runStoreQuery(t, sqlDB, "Toolbar", MatchFlags{})
	if len(loose) != 2 {
		t.Fatalf("flagless hits = %v, want both cases", storeSnippets(loose))
	}
	strict := runStoreQuery(t, sqlDB, "Toolbar", MatchFlags{CaseSensitive: true})
	if len(strict) != 1 || strict[0].Snippet != "Deploy Toolbar ready" {
		t.Fatalf("case-sensitive hits = %v, want only the exact-case row", storeSnippets(strict))
	}
}

func TestStoreExecutorWholeWordFiltersFreeText(t *testing.T) {
	// The post-filter distinguishes FTS tokens from word boundaries.
	sqlDB := seedStoreRows(t, "sess-word", []IndexRow{
		{ID: "ev-word", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "call foo here"},
		{ID: "ev-joined", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "call foo_bar here"},
	})
	loose := runStoreQuery(t, sqlDB, "foo", MatchFlags{})
	if len(loose) != 2 {
		t.Fatalf("flagless hits = %v, want both rows as FTS candidates", storeSnippets(loose))
	}
	whole := runStoreQuery(t, sqlDB, "foo", MatchFlags{WholeWord: true})
	if len(whole) != 1 || whole[0].Snippet != "call foo here" {
		t.Fatalf("whole-word hits = %v, want only the bounded row", storeSnippets(whole))
	}
}

func TestStoreExecutorRegexMatchesWithoutFTSPhraseMutation(t *testing.T) {
	sqlDB := seedStoreRows(t, "sess-regex", []IndexRow{
		{ID: "ev-timeout", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "error: request timeout"},
		{ID: "ev-fine", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "request finished fine"},
	})
	// Regex matching evaluates scoped rows without FTS mutation.
	hits := runStoreQuery(t, sqlDB, "err.*timeout", MatchFlags{Regex: true})
	if len(hits) != 1 || hits[0].Snippet != "error: request timeout" {
		t.Fatalf("regex hits = %v, want the row FTS phrase matching would miss", storeSnippets(hits))
	}
}

func TestStoreExecutorRegexKeepsFieldPredicatesInSQL(t *testing.T) {
	sqlDB := seedStoreRows(t, "sess-regex-filter", []IndexRow{
		{ID: "ev-web", Source: SourceTool, HitKind: HitKindWeb, URL: "https://example.test", Snippet: "error: request timeout"},
		{ID: "ev-plain", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "error: request timeout"},
	})
	plan, err := CompileQuery("err.*timeout kind:web", CompileContext{
		OriginProjectID: testdbseed.DefaultProjectID,
		Flags:           MatchFlags{Regex: true},
	})
	testutil.FailErr(t, "CompileQuery", err)
	if strings.Contains(plan.Store.SQL, "MATCH") {
		t.Fatalf("regex free text must not reach FTS MATCH: %s", plan.Store.SQL)
	}
	if !strings.Contains(plan.Store.SQL, "e.hit_kind = ?") {
		t.Fatalf("field predicate must stay in SQL: %s", plan.Store.SQL)
	}
	report, err := NewStoreExecutor(sqlDB).Run(context.Background(), PlanLeg{
		Executor: ExecutorStore, Cap: SearchExecutorProbeHits, Store: plan.Store,
	})
	testutil.FailErr(t, "StoreExecutor.Run", err)
	hits := report.Hits
	if len(hits) != 1 || hits[0].HitKind != HitKindWeb {
		t.Fatalf("regex+filter hits = %+v, want only the web row", storeSnippets(hits))
	}
}

func TestStoreExecutorInvalidRegexSharesCodeLegError(t *testing.T) {
	// Quoting preserves the bracket for regex validation.
	_, err := CompileQuery(`"a["`, CompileContext{
		OriginProjectID: testdbseed.DefaultProjectID,
		Flags:           MatchFlags{Regex: true},
	})
	if err == nil {
		t.Fatal("expected invalid regex to fail compile")
	}
	var me *MatchError
	if !errors.As(err, &me) {
		t.Fatalf("want MatchError (code leg parity), got %T: %v", err, err)
	}
}

func TestStoreExecutorMixedTextFilterOrHonorsFlags(t *testing.T) {
	sqlDB := seedStoreRows(t, "sess-mixed-flags", []IndexRow{
		{ID: "ev-alpha", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "alpha only"},
		{ID: "ev-loud", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "ALPHA loud"},
		{ID: "ev-web", Source: SourceTool, HitKind: HitKindWeb, URL: "https://example.test", Snippet: "unrelated web result"},
	})
	hits := runStoreQuery(t, sqlDB, "alpha OR kind:web", MatchFlags{CaseSensitive: true})
	if len(hits) != 2 || !containsSnippet(hits, "alpha only") || !containsSnippet(hits, "unrelated web result") {
		t.Fatalf("mixed OR hits = %v, want exact-case text arm plus filter arm", storeSnippets(hits))
	}
	if containsSnippet(hits, "ALPHA loud") {
		t.Fatalf("case-mismatched row must not survive via the filter arm: %v", storeSnippets(hits))
	}
}

func TestStoreExecutorGlobsFilterPathBearingHitsOnly(t *testing.T) {
	rows := []IndexRow{
		{ID: "ev-doc", Source: SourceTool, HitKind: HitKindEvidence, Path: "docs/notes.md", Snippet: "gamma delta"},
		{ID: "ev-src", Source: SourceTool, HitKind: HitKindEvidence, Path: "src/main.go", Snippet: "gamma delta"},
		{ID: "ev-msg", Source: SourceMessage, HitKind: HitKindMessage, Snippet: "gamma delta pathless"},
	}
	sqlDB := seedStoreRows(t, "sess-globs", rows)

	include := runStoreQuery(t, sqlDB, "gamma", MatchFlags{Include: []string{"**/*.go"}})
	if len(include) != 2 || !containsSnippet(include, "gamma delta pathless") {
		t.Fatalf("include hits = %v, want the .go row plus the pathless row", storeSnippets(include))
	}
	for _, hit := range include {
		if hit.Path != "" && hit.Path != "src/main.go" {
			t.Fatalf("include leaked path-bearing hit %q", hit.Path)
		}
	}

	exclude := runStoreQuery(t, sqlDB, "gamma", MatchFlags{Exclude: []string{"docs/**"}})
	if len(exclude) != 2 || !containsSnippet(exclude, "gamma delta pathless") {
		t.Fatalf("exclude hits = %v, want the .go row plus the pathless row", storeSnippets(exclude))
	}
	for _, hit := range exclude {
		if hit.Path == "docs/notes.md" {
			t.Fatal("exclude glob must drop the docs row")
		}
	}
}

func TestCompileStoreLegWithoutFreeTextIgnoresMatchFlags(t *testing.T) {
	plan, err := CompileQuery("kind:web", CompileContext{
		OriginProjectID: testdbseed.DefaultProjectID,
		Flags:           MatchFlags{Regex: true, CaseSensitive: true, WholeWord: true},
	})
	testutil.FailErr(t, "CompileQuery", err)
	if plan.Store == nil || plan.Store.Post != nil {
		t.Fatalf("flags without free text must not add a post filter: %+v", plan.Store)
	}
}

func TestPostFilterScansPastResultCap(t *testing.T) {
	// Newest rows all match FTS but fail the case-sensitive recheck; only the
	// oldest row truly matches. Bounding candidates at cap+1 would miss it.
	rows := make([]IndexRow, 0, 41)
	base := time.Now().UTC()
	for i := 0; i < 40; i++ {
		rows = append(rows, IndexRow{
			ID:      "loud-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Source:  SourceMessage,
			HitKind: HitKindMessage,
			Snippet: "NEEDLEX filler",
			TS:      base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
		})
	}
	rows = append(rows, IndexRow{
		ID:      "quiet-old",
		Source:  SourceMessage,
		HitKind: HitKindMessage,
		Snippet: "needlex exact",
		TS:      base.Add(-time.Hour).Format(time.RFC3339),
	})
	sqlDB := seedStoreRows(t, "sess-postfilter-depth", rows)
	plan, err := CompileQuery("needlex", CompileContext{
		OriginProjectID: testdbseed.DefaultProjectID,
		Flags:           MatchFlags{CaseSensitive: true},
	})
	testutil.FailErr(t, "CompileQuery", err)
	report, err := NewStoreExecutor(sqlDB).Run(context.Background(), PlanLeg{
		Executor: ExecutorStore, Cap: 5, Store: plan.Store,
	})
	testutil.FailErr(t, "StoreExecutor.Run", err)
	if !containsSnippet(report.Hits, "needlex exact") {
		t.Fatalf("old matching row missed: %v", storeSnippets(report.Hits))
	}
}

func TestStoreExecutorAppliesIndexLessTermsPerRow(t *testing.T) {
	sqlDB := seedStoreRows(t, "sess-punct", []IndexRow{
		{ID: "ev-arrow", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "alpha -> beta // note"},
		{ID: "ev-plain", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "alpha plain"},
	})
	// "//" has no FTS token; it must narrow by row text, not zero the query.
	hits := runStoreQuery(t, sqlDB, "alpha //", MatchFlags{})
	if len(hits) != 1 || hits[0].Snippet != "alpha -> beta // note" {
		t.Fatalf("hits = %v, want only the row containing //", storeSnippets(hits))
	}
	hits = runStoreQuery(t, sqlDB, "alpha NOT //", MatchFlags{})
	if len(hits) != 1 || hits[0].Snippet != "alpha plain" {
		t.Fatalf("hits = %v, want only the row without //", storeSnippets(hits))
	}
}

func TestStoreExecutorNegatedFreeTextExcludesRows(t *testing.T) {
	sqlDB := seedStoreRows(t, "sess-neg", []IndexRow{
		{ID: "ev-both", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "alpha and gamma"},
		{ID: "ev-alpha", Source: SourceTool, HitKind: HitKindEvidence, Snippet: "alpha only"},
	})
	hits := runStoreQuery(t, sqlDB, "alpha NOT gamma", MatchFlags{})
	if len(hits) != 1 || hits[0].Snippet != "alpha only" {
		t.Fatalf("hits = %v, want only the row without gamma", storeSnippets(hits))
	}
	hits = runStoreQuery(t, sqlDB, "alpha NOT Gamma", MatchFlags{CaseSensitive: true})
	if len(hits) != 2 {
		t.Fatalf("case-sensitive NOT Gamma must keep lowercase gamma rows: %v", storeSnippets(hits))
	}
}
