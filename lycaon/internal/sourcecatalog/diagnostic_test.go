package sourcecatalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiagnosticDetectsEachInjectedDisagreement(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/present.txt", "ok")
	writeIndexFile(t, root.Path, "src/missing_in_cat.txt", "injected missing")
	writeIndexFile(t, root.Path, "src/kind_flip.txt", "file on disk")
	reader := waitIndex(t, catalog, root)
	store := reader.store
	testutil.FailErr(t, "close initial reader", reader.Close())

	// Inject three disagreements directly into the index DB without advancing disk:
	// 1. missing_in_catalog: delete src/missing_in_cat.txt from nodes
	// 2. extra_in_catalog: insert ghost row src/ghost_in_cat.txt
	// 3. kind_mismatch: flip src/kind_flip.txt to is_dir=1, regular=0
	db, err := openTreeDB(t.Context(), store.file)
	testutil.FailErr(t, "open tree db", err)
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(t.Context(), "DELETE FROM nodes WHERE path='src/missing_in_cat.txt'")
	testutil.FailErr(t, "inject missing_in_catalog", err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO nodes(
		path,search_path,search_name,parent,name,depth,indexed,agent_metadata,first_listed,is_dir,symlink,regular,hidden,size,mode,modified,refused
	) VALUES('src/ghost_in_cat.txt','src/ghost_in_cat.txt','ghost_in_cat.txt','src','ghost_in_cat.txt',2,1,0,1,0,0,1,0,10,420,1,'')`)
	testutil.FailErr(t, "inject extra_in_catalog", err)
	_, err = db.ExecContext(t.Context(), "UPDATE nodes SET is_dir=1, regular=0 WHERE path='src/kind_flip.txt'")
	testutil.FailErr(t, "inject kind_mismatch", err)

	reportPath := filepath.Join(t.TempDir(), "catalog-diagnostic.jsonl")
	sampler := NewDiagnosticSampler()
	report, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, DiagnosticConfig{
		Enabled:    true,
		Subtree:    "src",
		ReportPath: reportPath,
	}, sampler)
	testutil.FailErr(t, "run diagnostic sample", err)

	want := []DiagnosticDisagreement{
		{Path: "src/ghost_in_cat.txt", Kind: DisagreementExtraInCatalog, CatalogKind: "file"},
		{Path: "src/kind_flip.txt", Kind: DisagreementKindMismatch, CatalogKind: "dir", DiskKind: "file"},
		{Path: "src/missing_in_cat.txt", Kind: DisagreementMissingInCatalog, DiskKind: "file"},
	}
	if !slices.Equal(report.Disagreements, want) {
		t.Fatalf("disagreements = %+v, want %+v", report.Disagreements, want)
	}
	if report.DiagnosticReads.DirReads == 0 || report.DiagnosticReads.EntryReads == 0 {
		t.Fatalf("expected separate diagnostic read counts > 0, got %+v", report.DiagnosticReads)
	}

	// Verify bounded JSONL output is valid typed JSON.
	raw, err := os.ReadFile(reportPath)
	testutil.FailErr(t, "read diagnostic jsonl", err)
	var decoded DiagnosticReport
	testutil.FailErr(t, "decode diagnostic jsonl", json.Unmarshal(raw, &decoded))
	if !slices.Equal(decoded.Disagreements, want) {
		t.Fatalf("decoded disagreements = %+v, want %+v", decoded.Disagreements, want)
	}
}

func TestDiagnosticRaisesNothingOnDeclaredGaps(t *testing.T) {
	t.Run("bounded_directory_and_lazy_boundary", func(t *testing.T) {
		catalog, root := indexFixture(t)
		catalog.SetScopes(testScopes{plane: sourcescope.Plane{Budgets: sandbox.SurveyBudgets{DirectoryEntries: 2}}})
		writeIndexFile(t, root.Path, "healthy.txt", "ok")
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			writeIndexFile(t, root.Path, "wide/"+name, "bounded")
		}
		writeIndexFile(t, root.Path, "node_modules/pkg/index.js", "lazy")
		reader := waitIndex(t, catalog, root)
		testutil.FailErr(t, "close reader", reader.Close())

		report, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, DiagnosticConfig{Enabled: true}, NewDiagnosticSampler())
		testutil.FailErr(t, "sample bounded catalog", err)
		if len(report.Disagreements) != 0 {
			t.Fatalf("expected 0 disagreements on bounded/lazy gaps, got %+v", report.Disagreements)
		}
	})

	t.Run("faulted_directory", func(t *testing.T) {
		catalog, root := indexFixture(t)
		writeIndexFile(t, root.Path, "good.txt", "ok")
		writeIndexFile(t, root.Path, "private/secret.txt", "ok")
		reader := waitIndex(t, catalog, root)
		store := reader.store
		testutil.FailErr(t, "close reader", reader.Close())

		// Record a declared fault on private and remove its child from nodes.
		db, err := openTreeDB(t.Context(), store.file)
		testutil.FailErr(t, "open db", err)
		defer func() { _ = db.Close() }()
		_, err = db.ExecContext(t.Context(), "INSERT OR REPLACE INTO faults(path) VALUES('private')")
		testutil.FailErr(t, "insert fault", err)
		_, err = db.ExecContext(t.Context(), "DELETE FROM nodes WHERE path='private/secret.txt'")
		testutil.FailErr(t, "delete faulted child", err)

		report, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, DiagnosticConfig{Enabled: true}, NewDiagnosticSampler())
		testutil.FailErr(t, "sample faulted catalog", err)
		if len(report.Disagreements) != 0 {
			t.Fatalf("expected 0 disagreements on faulted dir gap, got %+v", report.Disagreements)
		}
	})

	t.Run("warming_incomplete_refreshing_and_refresh_failed", func(t *testing.T) {
		catalog, root := indexFixture(t)
		writeIndexFile(t, root.Path, "good.txt", "ok")
		reader := waitIndex(t, catalog, root)
		store := reader.store
		testutil.FailErr(t, "close reader", reader.Close())

		// Drop good.txt from DB to prove declared root gaps suppress reporting.
		db, err := openTreeDB(t.Context(), store.file)
		testutil.FailErr(t, "open db", err)
		defer func() { _ = db.Close() }()
		_, err = db.ExecContext(t.Context(), "DELETE FROM nodes WHERE path='good.txt'")
		testutil.FailErr(t, "delete node", err)

		cases := []struct {
			name   string
			mutate func()
		}{
			{
				name: "warming",
				mutate: func() {
					store.mu.Lock()
					store.status = TreeStatus{State: StateWarming}
					store.mu.Unlock()
				},
			},
			{
				name: "incomplete",
				mutate: func() {
					_, err := db.ExecContext(t.Context(), "UPDATE meta SET complete=0 WHERE id=1")
					testutil.FailErr(t, "set meta incomplete", err)
					store.mu.Lock()
					store.status = TreeStatus{State: StateReady, Revision: 1, Complete: false}
					store.full = false
					store.mu.Unlock()
				},
			},
			{
				name: "refreshing",
				mutate: func() {
					_, err := db.ExecContext(t.Context(), "UPDATE meta SET complete=1 WHERE id=1")
					testutil.FailErr(t, "restore meta complete", err)
					store.mu.Lock()
					store.status = TreeStatus{State: StateReady, Revision: 1, Complete: true}
					store.full = true
					store.mu.Unlock()
				},
			},
			{
				name: "refresh_failed",
				mutate: func() {
					store.mu.Lock()
					store.status = TreeStatus{State: StateReady, Revision: 1, Complete: true, Error: "refresh failed"}
					store.full = false
					store.mu.Unlock()
				},
			},
		}

		for _, tc := range cases {
			tc.mutate()
			report, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, DiagnosticConfig{Enabled: true}, NewDiagnosticSampler())
			testutil.FailErr(t, "sample "+tc.name, err)
			if len(report.Disagreements) != 0 {
				t.Fatalf("%s: expected 0 disagreements, got %+v", tc.name, report.Disagreements)
			}
		}
	})
}

func TestDiagnosticReportIsBoundedAndDeduplicated(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := 0; i < 12; i++ {
		writeIndexFile(t, root.Path, fmt.Sprintf("f_%02d.txt", i), "data")
	}
	reader := waitIndex(t, catalog, root)
	store := reader.store
	testutil.FailErr(t, "close reader", reader.Close())

	db, err := openTreeDB(t.Context(), store.file)
	testutil.FailErr(t, "open db", err)
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(t.Context(), "DELETE FROM nodes WHERE regular=1")
	testutil.FailErr(t, "delete all files", err)

	reportPath := filepath.Join(t.TempDir(), "bounded.jsonl")
	sampler := NewDiagnosticSampler()
	cfg := DiagnosticConfig{
		Enabled:          true,
		MaxDisagreements: 5,
		ReportPath:       reportPath,
	}

	first, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, cfg, sampler)
	testutil.FailErr(t, "first sample", err)
	if len(first.Disagreements) != 5 || !first.Truncated || first.TotalObserved != 12 {
		t.Fatalf("first bounded report = len:%d truncated:%v total:%d", len(first.Disagreements), first.Truncated, first.TotalObserved)
	}

	// Second sample on the same generation deduplicates all 12 previously seen keys.
	second, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, cfg, sampler)
	testutil.FailErr(t, "second sample", err)
	if len(second.Disagreements) != 0 || second.DedupedCount != 12 {
		t.Fatalf("second deduped report = len:%d deduped:%d", len(second.Disagreements), second.DedupedCount)
	}

	f, err := os.Open(reportPath)
	testutil.FailErr(t, "open jsonl", err)
	defer func() { _ = f.Close() }()
	lines := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines++
	}
	if lines != 2 {
		t.Fatalf("expected 2 JSONL report lines, got %d", lines)
	}
}

func TestDiagnosticEnabledAndDisabledMatchAnswersAndProductWorkCounters(t *testing.T) {
	runOnce := func(diagEnabled bool) ([]string, int, IndexCoverage, WorkCounters, *DiagnosticReport) {
		catalog, root := indexFixture(t)
		writeIndexFile(t, root.Path, "alpha.txt", "a")
		writeIndexFile(t, root.Path, "sub/beta.txt", "b")
		writeIndexFile(t, root.Path, "sub/deep/gamma.txt", "c")

		reader := waitIndex(t, catalog, root)
		entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", 64)
		testutil.FailErr(t, "file page", err)
		count, err := reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
		testutil.FailErr(t, "file count", err)
		cov, err := reader.Coverage(t.Context())
		testutil.FailErr(t, "coverage", err)
		testutil.FailErr(t, "close reader", reader.Close())

		report, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, DiagnosticConfig{
			Enabled: diagEnabled,
		}, NewDiagnosticSampler())
		testutil.FailErr(t, "diagnostic sample", err)

		work, err := catalog.Trees.ProductWorkCounters(t.Context(), "p", root)
		testutil.FailErr(t, "product work counters", err)

		paths := make([]string, len(entries))
		for i, e := range entries {
			paths[i] = e.Path
		}
		return paths, count, cov, work, report
	}

	disabledPaths, disabledCount, disabledCov, disabledWork, disabledReport := runOnce(false)
	enabledPaths, enabledCount, enabledCov, enabledWork, enabledReport := runOnce(true)

	if disabledReport != nil {
		t.Fatalf("disabled diagnostic returned report: %+v", disabledReport)
	}
	if enabledReport == nil {
		t.Fatal("enabled diagnostic returned nil report")
	}
	if !slices.Equal(disabledPaths, enabledPaths) || disabledCount != enabledCount || disabledCov != enabledCov {
		t.Fatalf("product answers diverged: disabled=(%v,%d,%+v) enabled=(%v,%d,%+v)",
			disabledPaths, disabledCount, disabledCov, enabledPaths, enabledCount, enabledCov)
	}
	if disabledWork != enabledWork {
		t.Fatalf("ordinary product work counters diverged: disabled=%+v enabled=%+v", disabledWork, enabledWork)
	}
	if disabledWork.DirReads == 0 || disabledWork.MetadataReads == 0 || disabledWork.PublishedBatches == 0 || disabledWork.RowsRead == 0 {
		t.Fatalf("expected non-zero ordinary product work counters, got %+v", disabledWork)
	}
	if enabledReport.DiagnosticReads.DirReads == 0 || enabledReport.DiagnosticReads.EntryReads == 0 {
		t.Fatalf("expected separate diagnostic read counts > 0, got %+v", enabledReport.DiagnosticReads)
	}
	if len(enabledReport.Disagreements) != 0 {
		t.Fatalf("healthy tree raised disagreements: %+v", enabledReport.Disagreements)
	}
}
