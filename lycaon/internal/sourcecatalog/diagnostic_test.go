package sourcecatalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
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
	if !report.GenerationConsistent {
		t.Fatal("expected GenerationConsistent=true on settled tree")
	}
	if !slices.Equal(report.Disagreements, want) {
		t.Fatalf("disagreements = %+v, want %+v", report.Disagreements, want)
	}
	if report.DiagnosticReads.DirReads == 0 || report.DiagnosticReads.EntryReads == 0 || report.DiagnosticReads.CatalogRowsRead == 0 {
		t.Fatalf("expected separate diagnostic read counts > 0, got %+v", report.DiagnosticReads)
	}

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

func TestDiagnosticRetainedReportDoesNotGrowOnCleanOrDuplicateSamples(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := 0; i < 12; i++ {
		writeIndexFile(t, root.Path, fmt.Sprintf("f_%02d.txt", i), "data")
	}
	reader := waitIndex(t, catalog, root)
	store := reader.store
	testutil.FailErr(t, "close reader", reader.Close())

	reportPath := filepath.Join(t.TempDir(), "bounded.jsonl")
	sampler := NewDiagnosticSampler()
	cfg := DiagnosticConfig{
		Enabled:            true,
		MaxDisagreements:   5,
		MaxRetainedReports: 3,
		ReportPath:         reportPath,
	}

	for i := 0; i < 5; i++ {
		clean, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, cfg, sampler)
		testutil.FailErr(t, "clean sample", err)
		if len(clean.Disagreements) != 0 {
			t.Fatalf("clean sample raised disagreements: %+v", clean.Disagreements)
		}
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("expected no report file written on clean samples, stat err=%v", err)
	}

	db, err := openTreeDB(t.Context(), store.file)
	testutil.FailErr(t, "open db", err)
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(t.Context(), "DELETE FROM nodes WHERE regular=1")
	testutil.FailErr(t, "delete all files", err)

	first, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, cfg, sampler)
	testutil.FailErr(t, "first sample", err)
	if len(first.Disagreements) != 5 || !first.Truncated || first.TotalObserved != 12 {
		t.Fatalf("first bounded report = len:%d truncated:%v total:%d", len(first.Disagreements), first.Truncated, first.TotalObserved)
	}
	info1, err := os.Stat(reportPath)
	testutil.FailErr(t, "stat after first write", err)

	for i := 0; i < 4; i++ {
		dup, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, cfg, sampler)
		testutil.FailErr(t, "duplicate sample", err)
		if len(dup.Disagreements) != 0 || dup.DedupedCount != 12 {
			t.Fatalf("duplicate sample = len:%d deduped:%d", len(dup.Disagreements), dup.DedupedCount)
		}
	}
	info2, err := os.Stat(reportPath)
	testutil.FailErr(t, "stat after duplicate samples", err)
	if info2.Size() != info1.Size() {
		t.Fatalf("report file grew on duplicate samples: %d -> %d", info1.Size(), info2.Size())
	}

	for rev := uint64(2); rev <= 6; rev++ {
		_, err = db.ExecContext(t.Context(), "UPDATE meta SET revision=? WHERE id=1", rev)
		testutil.FailErr(t, "bump meta revision", err)
		store.mu.Lock()
		store.status.Revision = rev
		store.mu.Unlock()
		_, err = catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, cfg, sampler)
		testutil.FailErr(t, "sample distinct generation", err)
	}

	f, err := os.Open(reportPath)
	testutil.FailErr(t, "open jsonl", err)
	defer func() { _ = f.Close() }()
	lines := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines++
	}
	if lines != 3 {
		t.Fatalf("expected retained report capped at 3 lines, got %d", lines)
	}
}

func TestDiagnosticWideDirectoryAndLargeCatalogRemainBounded(t *testing.T) {
	catalog, root := indexFixture(t)
	// Unrelated large catalog (>1,000 files).
	for d := 0; d < 25; d++ {
		for f := 0; f < 40; f++ {
			writeIndexFile(t, root.Path, fmt.Sprintf("unrelated_%02d/file_%02d.txt", d, f), "x")
		}
	}
	// Single wide directory with 120 files.
	for f := 0; f < 120; f++ {
		writeIndexFile(t, root.Path, fmt.Sprintf("wide_pkg/entry_%03d.txt", f), "w")
	}
	reader := waitIndex(t, catalog, root)
	testutil.FailErr(t, "close reader", reader.Close())

	report, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, DiagnosticConfig{
		Enabled:    true,
		Subtree:    "wide_pkg",
		MaxDirs:    4,
		MaxEntries: 16,
	}, NewDiagnosticSampler())
	testutil.FailErr(t, "sample wide directory in large catalog", err)

	if len(report.Disagreements) != 0 {
		t.Fatalf("healthy wide directory raised false disagreements: %+v", report.Disagreements)
	}
	if report.DiagnosticReads.DirReads != 1 || report.DiagnosticReads.EntryReads > 16 {
		t.Fatalf("wide directory exceeded bounded filesystem reads: %+v", report.DiagnosticReads)
	}
	if report.DiagnosticReads.CatalogRowsRead > 25 {
		t.Fatalf("wide directory scanned too many catalog rows: %d (want <= 25)", report.DiagnosticReads.CatalogRowsRead)
	}
}

func TestDiagnosticSymmetricTruncationGuardPreventsFalseMissing(t *testing.T) {
	catalog, root := indexFixture(t)
	// Create a healthy tree with enough entries that MaxEntries=6 truncates SQL
	// (ordered by path: a_00..a_04, then b_00..b_09) while the directory walk
	// may observe entries past the SQL frontier.
	for i := 0; i < 20; i++ {
		writeIndexFile(t, root.Path, fmt.Sprintf("pkg/item_%02d.txt", i), "ok")
	}
	writeIndexFile(t, root.Path, "pkg/zzz_late.txt", "ok")
	reader := waitIndex(t, catalog, root)
	testutil.FailErr(t, "close reader", reader.Close())

	report, err := catalog.Trees.SampleCatalogDiagnostic(t.Context(), "p", root, DiagnosticConfig{
		Enabled:    true,
		Subtree:    "pkg",
		MaxDirs:    4,
		MaxEntries: 6,
	}, NewDiagnosticSampler())
	testutil.FailErr(t, "sample truncated healthy subtree", err)

	if len(report.Disagreements) != 0 {
		t.Fatalf("bounded catalog truncation raised false disagreements on healthy tree: %+v", report.Disagreements)
	}
}

func TestDiagnosticConcurrentReportPublicationAndWriteFailure(t *testing.T) {
	reportDir := t.TempDir()
	reportPath := filepath.Join(reportDir, "concurrent-diagnostic.jsonl")
	sampler := NewDiagnosticSampler()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			rep := &DiagnosticReport{
				ProjectID:            fmt.Sprintf("p-%d", idx),
				RootID:               fmt.Sprintf("r-%d", idx),
				GenerationInstance:   uint64(idx + 1),
				GenerationRevision:   1,
				GenerationConsistent: true,
				Subtree:              ".",
				Disagreements: []DiagnosticDisagreement{
					{Path: fmt.Sprintf("f_%d.txt", idx), Kind: DisagreementMissingInCatalog, DiskKind: "file"},
				},
			}
			if err := sampler.retainAndWrite(reportPath, rep, nil, 4); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "concurrent retainAndWrite", err)
	}

	raw, err := os.ReadFile(reportPath)
	testutil.FailErr(t, "read concurrent jsonl", err)
	lines := 0
	sc := bufio.NewScanner(os.NewFile(0, ""))
	_ = sc
	for _, line := range slices.DeleteFunc(splitLines(string(raw)), func(s string) bool { return s == "" }) {
		var dec DiagnosticReport
		testutil.FailErr(t, "parse concurrent jsonl line", json.Unmarshal([]byte(line), &dec))
		lines++
	}
	if lines != 4 {
		t.Fatalf("expected 4 atomically retained reports, got %d", lines)
	}

	// Write failure handling: attempting to replace an existing directory must fail
	// cleanly, leave sampler.retained unchanged, AND NOT mark the disagreement seen
	// so a subsequent write to a valid path publishes that same disagreement.
	badTarget := filepath.Join(reportDir, "existing_dir")
	testutil.FailErr(t, "mkdir bad target", os.MkdirAll(badTarget, 0o700))
	beforeLen := len(sampler.retained)
	failReport := &DiagnosticReport{
		ProjectID:          "p-fail",
		RootID:             "r-fail",
		GenerationInstance: 99,
		GenerationRevision: 1,
		Disagreements:      []DiagnosticDisagreement{{Path: "x.txt", Kind: DisagreementMissingInCatalog, DiskKind: "file"}},
	}
	failErr := sampler.retainAndWrite(badTarget, failReport, nil, 4)
	if failErr == nil {
		t.Fatal("expected atomic publication error when target is a directory")
	}
	if len(sampler.retained) != beforeLen {
		t.Fatalf("failed write mutated retained snapshot: %d -> %d", beforeLen, len(sampler.retained))
	}

	// Retry the exact same disagreement to a valid path: it must NOT be discarded as duplicate.
	retryPath := filepath.Join(reportDir, "recovered-diagnostic.jsonl")
	retryReport := &DiagnosticReport{
		ProjectID:          "p-fail",
		RootID:             "r-fail",
		GenerationInstance: 99,
		GenerationRevision: 1,
		Disagreements:      []DiagnosticDisagreement{{Path: "x.txt", Kind: DisagreementMissingInCatalog, DiskKind: "file"}},
	}
	testutil.FailErr(t, "retry after write failure", sampler.retainAndWrite(retryPath, retryReport, nil, 4))
	if len(retryReport.Disagreements) != 1 || retryReport.DedupedCount != 0 {
		t.Fatalf("failed-write disagreement was wrongly deduplicated on retry: len=%d deduped=%d", len(retryReport.Disagreements), retryReport.DedupedCount)
	}
	recoveredRaw, err := os.ReadFile(retryPath)
	testutil.FailErr(t, "read recovered jsonl", err)
	if len(recoveredRaw) == 0 {
		t.Fatal("expected recovered report bytes on retry")
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func TestDiagnosticSkipsComparisonWhenLiveChangeOccursDuringSample(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/a.txt", "a")
	reader := waitIndex(t, catalog, root)
	testutil.FailErr(t, "close reader", reader.Close())

	writeIndexFile(t, root.Path, "src/concurrent_new.txt", "new")
	workBefore, err := catalog.Trees.ProductWorkCounters(t.Context(), "p", root)
	testutil.FailErr(t, "work before", err)

	report, err := catalog.Trees.sampleCatalogDiagnosticWithHook(
		t.Context(),
		"p",
		root,
		DiagnosticConfig{Enabled: true, Subtree: "src"},
		NewDiagnosticSampler(),
		func() {
			repochange.Advance(root.Path)
		},
	)
	testutil.FailErr(t, "sample with concurrent live change", err)

	if report.GenerationConsistent {
		t.Fatal("expected GenerationConsistent=false when live change occurred during sample")
	}
	if len(report.Disagreements) != 0 {
		t.Fatalf("live change during sample must not raise false disagreements, got %+v", report.Disagreements)
	}
	workAfter, err := catalog.Trees.ProductWorkCounters(t.Context(), "p", root)
	testutil.FailErr(t, "work after", err)
	if workAfter != workBefore {
		t.Fatalf("diagnostic modified ordinary product work counters: before=%+v after=%+v", workBefore, workAfter)
	}
}

func TestDiagnosticDevBoundaryAndAsyncDispatch(t *testing.T) {
	t.Run("disabled_without_LYCAON_DEV", func(t *testing.T) {
		t.Setenv("LYCAON_DEV", "")
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC", "1")
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_FILE", filepath.Join(t.TempDir(), "should-not-run.jsonl"))

		catalog, root := indexFixture(t)
		writeIndexFile(t, root.Path, "src/a.txt", "a")
		reader := waitIndex(t, catalog, root)
		testutil.FailErr(t, "close reader", reader.Close())
		testutil.FailErr(t, "drain catalog", catalog.Drain(t.Context()))

		catalog.Trees.diagMu.Lock()
		samplerInit := catalog.Trees.diagSampler != nil
		catalog.Trees.diagMu.Unlock()
		if samplerInit {
			t.Fatal("diagnostic must remain disabled when LYCAON_DEV is not set")
		}
	})

	t.Run("enabled_with_LYCAON_DEV_dispatches_off_critical_path", func(t *testing.T) {
		reportPath := filepath.Join(t.TempDir(), "dev-optin-diagnostic.jsonl")
		t.Setenv("LYCAON_DEV", "1")
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC", "1")
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_FILE", reportPath)
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_SUBTREE", "src")

		catalog, root := indexFixture(t)
		writeIndexFile(t, root.Path, "src/a.txt", "a")
		reader := waitIndex(t, catalog, root)
		testutil.FailErr(t, "close reader", reader.Close())
		testutil.FailErr(t, "drain catalog", catalog.Drain(t.Context()))

		catalog.Trees.diagMu.Lock()
		samplerInit := catalog.Trees.diagSampler != nil
		catalog.Trees.diagMu.Unlock()
		if !samplerInit {
			t.Fatal("expected LYCAON_DEV=1 + LYCAON_CATALOG_DIAGNOSTIC=1 to dispatch diagnostic sampler")
		}
		if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
			t.Fatalf("clean normal reconcile should not write report file, stat err=%v", err)
		}
	})
}

func TestDiagnosticLifecycleDetachAndDrainWinWithoutRecreatingStore(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "detach-barrier.jsonl")
	t.Setenv("LYCAON_DEV", "1")
	t.Setenv("LYCAON_CATALOG_DIAGNOSTIC", "1")
	t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_FILE", reportPath)
	t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_SUBTREE", "src")

	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/a.txt", "a")
	reader := waitIndex(t, catalog, root)
	store := reader.store
	testutil.FailErr(t, "close reader", reader.Close())
	testutil.FailErr(t, "drain initial diagnostic", catalog.Drain(t.Context()))

	// Deterministic barrier inside the async diagnostic sample.
	entered := make(chan struct{})
	release := make(chan struct{})
	store.mu.Lock()
	store.retired = false
	store.diagHook = func() {
		close(entered)
		<-release
	}
	published := store.status
	store.mu.Unlock()

	// Schedule off critical path: must return immediately before barrier releases.
	store.scheduleDiagnostic(published)
	<-entered

	// Retire and remove the store from catalog.Trees while the diagnostic is paused at the barrier.
	catalog.Trees.mu.Lock()
	for k, s := range catalog.Trees.trees {
		if s == store {
			delete(catalog.Trees.trees, k)
		}
	}
	catalog.Trees.mu.Unlock()
	if !retireTreeStore(store) {
		t.Fatal("expected retireTreeStore to succeed on detached store")
	}

	// Capture diagDone so we verify clean exit after releasing barrier.
	store.mu.Lock()
	done := store.diagDone
	store.mu.Unlock()
	close(release)
	if done != nil {
		<-done
	}

	// Detached store must NOT be recreated in catalog.Trees and no report written.
	catalog.Trees.mu.Lock()
	remaining := len(catalog.Trees.trees)
	catalog.Trees.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("detached store was recreated in catalog.Trees: count=%d", remaining)
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("retired diagnostic sample should not write report file, stat err=%v", err)
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
	if enabledReport.DiagnosticReads.DirReads == 0 || enabledReport.DiagnosticReads.EntryReads == 0 || enabledReport.DiagnosticReads.CatalogRowsRead == 0 {
		t.Fatalf("expected separate diagnostic read counts > 0, got %+v", enabledReport.DiagnosticReads)
	}
	if len(enabledReport.Disagreements) != 0 {
		t.Fatalf("healthy tree raised disagreements: %+v", enabledReport.Disagreements)
	}
}
