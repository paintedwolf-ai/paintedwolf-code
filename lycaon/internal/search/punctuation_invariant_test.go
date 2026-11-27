package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBarePunctuationParsesAndMatchesBothLegs(t *testing.T) {
	// 1. Every advertised bare operator in isBareOperatorToken must parse cleanly
	// as a single literal text token, both standalone and alongside filters.
	advertisedOps := []string{
		"==", "===", "!=", "!==", "<=", ">=", "::", ":=", "=>",
		"&&", "||", "??", "<<", ">>", "+=", "%=", "...",
	}
	for _, op := range advertisedOps {
		for _, q := range []string{op, "project:current kind:code " + op} {
			node, err := ParseQuery(q)
			if err != nil {
				t.Fatalf("ParseQuery(%q) failed: %v", q, err)
			}
			got := textTerms(node)
			if len(got) != 1 || got[0] != op {
				t.Fatalf("ParseQuery(%q) terms = %v, want [%q]", q, got, op)
			}
		}
	}

	// 2. Positive literal match + negative control on the CODE leg for every advertised operator.
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := sourcecatalog.New()
	t.Cleanup(func() { _ = catalog.Drain(context.Background()) })

	root := t.TempDir()
	posBody := "package p\n// ops: == === != !== <= >= :: := => && || ?? << >> += %= ...\n"
	testutil.FailErr(t, "write positive file", os.WriteFile(filepath.Join(root, "positive.go"), []byte(posBody), 0o644))
	testutil.FailErr(t, "write negative file", os.WriteFile(filepath.Join(root, "negative.go"), []byte("package p\n\nfunc plainWordsOnly(a int) int { return a }\n"), 0o644))
	testutil.FailErr(t, "await catalog", catalogtest.AwaitIndex(t.Context(), catalog, "p-punct", sourcecatalog.Root{ID: "r-punct", Path: root}))

	roots := []CodeRoot{{ProjectID: "p-punct", RootID: "r-punct", Path: root}}
	for _, op := range advertisedOps {
		planCode, err := CompileQuery("project:current kind:code "+op, CompileContext{
			OriginProjectID: "p-punct",
			RootsForProject: func(string) ([]CodeRoot, error) { return roots, nil },
			Budget:          BudgetComplete,
		})
		testutil.FailErr(t, "compile code "+op, err)
		if planCode.Code == nil {
			t.Fatalf("expected code leg for bare %q query", op)
		}
		repCode, err := (&CodeExecutor{catalog: catalog, rerank: decide.Reranker{}}).Run(context.Background(), PlanLeg{
			Executor: ExecutorCode,
			Cap:      100,
			Code:     planCode.Code,
		})
		testutil.FailErr(t, "run code "+op, err)
		codePaths := hitPathsOfKind(repCode.Hits, HitKindCode)
		if len(codePaths) != 1 || codePaths[0] != "positive.go" {
			t.Fatalf("code leg %q hits = %v, want only [positive.go]", op, codePaths)
		}
	}

	// 3. Positive literal match + negative control on the STORE leg for every advertised operator.
	dbDir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dbDir, "punct.db"))
	projID := testdbseed.DefaultProjectID
	const sessID = "sess-punct"
	testdbseed.InsertSessionWithRoot(t, sqlDB, sessID, projID, dbDir)

	insertMsg := func(id string, seq int64, content string) {
		ts := time.Now().UTC()
		tx, err := sqlDB.BeginTx(t.Context(), nil)
		testutil.FailErr(t, "begin", err)
		testdbseed.InsertSessionEntry(t, tx, "ent-"+id, sessID, "utterance", id, seq)
		_, err = tx.ExecContext(t.Context(), `INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, kind, ts)
			VALUES (?, ?, ?, 'user', ?, 'user', 'user', 'trusted', '', ?)`,
			id, "ent-"+id, sessID, content, timelayout.Format(ts))
		testutil.FailErr(t, "insert msg", err)
		testutil.FailErr(t, "write-through", SyncMessageWriteThrough(t.Context(), tx, projID, sessID, api.Message{
			ID: id, Role: api.MessageRoleUser, Content: content, CreatedAt: ts,
		}))
		testutil.FailErr(t, "commit", tx.Commit())
	}
	insertMsg("msg-pos", 1, "ops == === != !== <= >= :: := => && || ?? << >> += %= ...")
	insertMsg("msg-neg", 2, "plain words only without operators")

	for _, op := range advertisedOps {
		planStore, err := CompileQuery("project:current kind:message "+op, CompileContext{
			OriginProjectID: projID,
			StoreOnly:       true,
		})
		testutil.FailErr(t, "compile store "+op, err)
		if planStore.Store == nil {
			t.Fatalf("expected store leg for bare %q query", op)
		}
		repStore, err := NewStoreExecutor(sqlDB).Run(t.Context(), PlanLeg{
			Executor: ExecutorStore,
			Cap:      50,
			Store:    planStore.Store,
		})
		testutil.FailErr(t, "run store "+op, err)
		if len(repStore.Hits) != 1 || repStore.Hits[0].SourceRef != "msg-pos" {
			t.Fatalf("store leg %q hits = %+v, want only msg-pos", op, repStore.Hits)
		}
	}
}

func TestBarePunctuationPreservesGrammarErrorsAndFlags(t *testing.T) {
	// Empty/grammar-only inputs must still fail with typed ParseError.
	for _, q := range []string{"", "   ", `"`, "()", "AND", "OR", "NOT"} {
		_, err := ParseQuery(q)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("ParseQuery(%q) = %v, want ParseError", q, err)
		}
	}

	// Closed-field validation still fails positionally even alongside bare punctuation.
	_, err := CompileQuery("kind:bogus ==", CompileContext{OriginProjectID: "p1", StoreOnly: true})
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Kind != ParseErrInvalidValue || pe.Field != "kind" {
		t.Fatalf("CompileQuery('kind:bogus ==') = %v, want invalid kind ParseError", err)
	}

	// Existing operators, filters, and quoted phrases remain intact.
	plan, err := CompileQuery(`project:current (alpha OR "exact phrase") NOT path:vendor/`, CompileContext{
		OriginProjectID: "p1",
		StoreOnly:       true,
	})
	testutil.FailErr(t, "compile regression query", err)
	if plan.Scope != ScopeCurrent || len(plan.Interpretation.FTSTerms) != 2 {
		t.Fatalf("unexpected plan interpretation: %+v", plan.Interpretation)
	}

	// Regex and whole-word flags still compile cleanly with bare punctuation.
	for _, flags := range []MatchFlags{{Regex: true}, {WholeWord: true}, {CaseSensitive: true}} {
		_, err := CompileQuery("==", CompileContext{
			OriginProjectID: "p1",
			Flags:           flags,
			StoreOnly:       true,
		})
		if err != nil && !strings.Contains(err.Error(), "regex") {
			t.Fatalf("CompileQuery('==', %+v) failed: %v", flags, err)
		}
	}
}
