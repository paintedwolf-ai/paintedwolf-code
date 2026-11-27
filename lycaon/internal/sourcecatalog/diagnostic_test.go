package sourcecatalog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
			if err := sampler.retainAndWrite(t.Context(), reportPath, rep, nil, 4, nil); err != nil {
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
	failErr := sampler.retainAndWrite(t.Context(), badTarget, failReport, nil, 4, nil)
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
	testutil.FailErr(t, "retry after write failure", sampler.retainAndWrite(t.Context(), retryPath, retryReport, nil, 4, nil))
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

func TestDiagnosticRealDrainSealsDelayedPublicationAndWaitsForInFlight(t *testing.T) {
	t.Run("drain_seals_delayed_publication_and_waits_for_inflight_job", func(t *testing.T) {
		reportPath := filepath.Join(t.TempDir(), "drain-seal.jsonl")
		t.Setenv("LYCAON_DEV", "1")
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC", "1")
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_FILE", reportPath)
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_SUBTREE", "src")

		catalog, rootA := indexFixture(t)
		rootB := Root{ID: "root-b", Path: t.TempDir()}
		writeIndexFile(t, rootA.Path, "src/a.txt", "a")
		writeIndexFile(t, rootB.Path, "src/b.txt", "b")

		storeA, err := catalog.Trees.indexStore(t.Context(), "p", rootA)
		testutil.FailErr(t, "attach storeA", err)
		storeB, err := catalog.Trees.indexStore(t.Context(), "p", rootB)
		testutil.FailErr(t, "attach storeB", err)

		// Hold storeB's diagnostic job in flight at a deterministic barrier.
		inFlightEntered := make(chan struct{})
		releaseInFlight := make(chan struct{})
		storeB.mu.Lock()
		storeB.diagHook = func() {
			close(inFlightEntered)
			<-releaseInFlight
		}
		storeB.mu.Unlock()

		// Delay storeA's final publication right after publishFinalLocked releases
		// storeA.mu and right before scheduleDiagnostic attempts admission.
		pubPaused := make(chan struct{})
		releasePub := make(chan struct{})
		var storeAAdmitted atomic.Bool
		storeA.mu.Lock()
		storeA.diagBeforeAdmit = func() {
			close(pubPaused)
			<-releasePub
		}
		storeA.diagHook = func() {
			storeAAdmitted.Store(true)
		}
		storeA.mu.Unlock()

		// 1. Warm rootB so its diagnostic enters the in-flight barrier.
		readerB := waitIndex(t, catalog, rootB)
		testutil.FailErr(t, "close readerB", readerB.Close())
		<-inFlightEntered

		// 2. Warm rootA so its final publication pauses right before scheduleDiagnostic.
		readerA := waitIndex(t, catalog, rootA)
		testutil.FailErr(t, "close readerA", readerA.Close())
		<-pubPaused

		// 3. Call the REAL TreeStores.Drain while storeA is paused before admission
		// and storeB has an in-flight diagnostic job.
		drainDone := make(chan error, 1)
		go func() {
			drainDone <- catalog.Trees.Drain(t.Context())
		}()

		// Wait until Drain has sealed storeA.pins.drained under storeA.mu.
		deadline := time.Now().Add(2 * time.Second)
		for {
			storeA.mu.Lock()
			sealed := storeA.pins.drained
			storeA.mu.Unlock()
			if sealed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for TreeStores.Drain to seal storeA")
			}
			time.Sleep(2 * time.Millisecond)
		}

		// Release storeA's delayed publication AFTER Drain sealed admission.
		// Drain must still wait for storeB's in-flight diagnostic.
		close(releasePub)
		select {
		case err := <-drainDone:
			t.Fatalf("TreeStores.Drain returned while storeB diagnostic was still in flight (err=%v)", err)
		case <-time.After(30 * time.Millisecond):
		}

		// Release storeB's in-flight diagnostic; Drain must now complete cleanly.
		close(releaseInFlight)
		select {
		case err := <-drainDone:
			testutil.FailErr(t, "TreeStores.Drain", err)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for TreeStores.Drain to join in-flight diagnostic")
		}

		if storeAAdmitted.Load() {
			t.Fatal("delayed publication admitted a diagnostic after TreeStores.Drain sealed the store")
		}
		storeA.mu.Lock()
		aDone := storeA.diagDone
		storeA.mu.Unlock()
		if aDone != nil {
			t.Fatal("storeA has an active diagnostic after TreeStores.Drain returned")
		}
		catalog.Trees.diagMu.Lock()
		stillActive := catalog.Trees.diagActive
		catalog.Trees.diagMu.Unlock()
		if stillActive {
			t.Fatal("global diagnostic slot remained active after TreeStores.Drain returned")
		}
	})

	t.Run("release_tree_root_cancels_and_joins_without_recreating_store", func(t *testing.T) {
		reportPath := filepath.Join(t.TempDir(), "release-root.jsonl")
		t.Setenv("LYCAON_DEV", "1")
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC", "1")
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_FILE", reportPath)
		t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_SUBTREE", "src")

		catalog, root := indexFixture(t)
		writeIndexFile(t, root.Path, "src/a.txt", "a")
		store, err := catalog.Trees.indexStore(t.Context(), "p", root)
		testutil.FailErr(t, "attach store", err)

		entered := make(chan struct{})
		release := make(chan struct{})
		store.mu.Lock()
		store.diagHook = func() {
			close(entered)
			<-release
		}
		store.mu.Unlock()

		reader := waitIndex(t, catalog, root)
		testutil.FailErr(t, "close reader", reader.Close())
		<-entered

		// Call REAL ReleaseTreeRoot while the diagnostic is paused at the barrier.
		releaseDone := make(chan error, 1)
		go func() {
			releaseDone <- catalog.Trees.ReleaseTreeRoot(t.Context(), root.Path)
		}()

		select {
		case err := <-releaseDone:
			t.Fatalf("ReleaseTreeRoot returned while diagnostic was still in flight (err=%v)", err)
		case <-time.After(30 * time.Millisecond):
		}

		close(release)
		select {
		case err := <-releaseDone:
			testutil.FailErr(t, "ReleaseTreeRoot", err)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for ReleaseTreeRoot to join diagnostic")
		}

		catalog.Trees.mu.Lock()
		remaining := len(catalog.Trees.trees)
		catalog.Trees.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("detached store was recreated in catalog.Trees: count=%d", remaining)
		}
		if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
			t.Fatalf("released root diagnostic should not write report file, stat err=%v", err)
		}
	})
}

func TestDiagnosticGlobalSingleSlotBoundsConcurrentRoots(t *testing.T) {
	t.Setenv("LYCAON_DEV", "1")
	t.Setenv("LYCAON_CATALOG_DIAGNOSTIC", "1")
	t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_FILE", filepath.Join(t.TempDir(), "global-slot.jsonl"))
	t.Setenv("LYCAON_CATALOG_DIAGNOSTIC_SUBTREE", "src")

	catalog, root0 := indexFixture(t)
	roots := []Root{
		root0,
		{ID: "r1", Path: t.TempDir()},
		{ID: "r2", Path: t.TempDir()},
		{ID: "r3", Path: t.TempDir()},
	}
	stores := make([]*indexStore, len(roots))
	var currentActive atomic.Int32
	var maxActive atomic.Int32
	var totalEntered atomic.Int32
	firstEntered := make(chan struct{})
	var firstOnce sync.Once
	releaseFirst := make(chan struct{})

	for i, r := range roots {
		writeIndexFile(t, r.Path, "src/file.txt", "ok")
		s, err := catalog.Trees.indexStore(t.Context(), "p", r)
		testutil.FailErr(t, "attach store", err)
		s.mu.Lock()
		s.diagHook = func() {
			n := currentActive.Add(1)
			totalEntered.Add(1)
			for {
				prev := maxActive.Load()
				if n <= prev || maxActive.CompareAndSwap(prev, n) {
					break
				}
			}
			firstOnce.Do(func() { close(firstEntered) })
			<-releaseFirst
			currentActive.Add(-1)
		}
		s.mu.Unlock()
		stores[i] = s
	}

	// Warm root0 so its diagnostic holds the single global slot.
	reader0 := waitIndex(t, catalog, roots[0])
	testutil.FailErr(t, "close reader0", reader0.Close())
	<-firstEntered

	// Simultaneously publish/schedule on roots 1..3 while root0 holds the global slot.
	var wg sync.WaitGroup
	for i := 1; i < len(roots); i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			rd := waitIndex(t, catalog, roots[idx])
			testutil.FailErr(t, "close concurrent reader", rd.Close())
			stores[idx].mu.Lock()
			pub := stores[idx].status
			stores[idx].mu.Unlock()
			stores[idx].scheduleDiagnostic(pub)
		}()
	}
	wg.Wait()

	if maxActive.Load() != 1 {
		t.Fatalf("global diagnostic concurrency exceeded 1: maxActive=%d", maxActive.Load())
	}
	if totalEntered.Load() != 1 {
		t.Fatalf("expected only 1 root diagnostic admitted while global slot was held, got %d", totalEntered.Load())
	}

	close(releaseFirst)
	testutil.FailErr(t, "drain catalog", catalog.Drain(t.Context()))
}

func TestDiagnosticCancellationInWalkAndBeforeReportPublish(t *testing.T) {
	t.Run("walk_stops_on_context_cancellation", func(t *testing.T) {
		rootDir := t.TempDir()
		for d := 0; d < 4; d++ {
			for f := 0; f < 5; f++ {
				writeIndexFile(t, rootDir, fmt.Sprintf("d%d/f%d.txt", d, f), "x")
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, _, _, reads, _, err := walkDirectSubtree(
			ctx,
			rootDir,
			DiagnosticConfig{Enabled: true, Subtree: ".", MaxDirs: 64, MaxEntries: 1024, MaxDepth: 8}.normalized(),
			walkPolicy{},
			map[string]struct{}{},
			map[string]string{},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled from walkDirectSubtree, got %v", err)
		}
		if reads.DirReads != 0 || reads.EntryReads != 0 {
			t.Fatalf("expected zero reads on pre-canceled walk, got %+v", reads)
		}
	})

	t.Run("publish_admission_unblocks_on_cancel_and_preserves_admitted_writer_order", func(t *testing.T) {
		reportPath := filepath.Join(t.TempDir(), "cancel-publish.jsonl")
		sampler := NewDiagnosticSampler()

		writerEntered := make(chan struct{})
		releaseWriter := make(chan struct{})
		var hookOnce sync.Once
		sampler.beforeWrite = func() {
			hookOnce.Do(func() {
				close(writerEntered)
				<-releaseWriter
			})
		}

		// Writer 1 is already admitted and holds the sampler lock at beforeWrite.
		rep1 := &DiagnosticReport{
			ProjectID:          "p1",
			RootID:             "r1",
			GenerationInstance: 1,
			GenerationRevision: 1,
			Disagreements:      []DiagnosticDisagreement{{Path: "first.txt", Kind: DisagreementMissingInCatalog, DiskKind: "file"}},
		}
		writer1Done := make(chan error, 1)
		go func() {
			writer1Done <- sampler.retainAndWrite(t.Context(), reportPath, rep1, nil, 8, func() bool { return true })
		}()
		<-writerEntered

		// Writer 2 waits on cancellation-aware admission; canceling ctx2 must unblock it
		// immediately WITHOUT waiting for Writer 1 to release.
		ctx2, cancel2 := context.WithCancel(t.Context())
		rep2 := &DiagnosticReport{
			ProjectID:          "p2",
			RootID:             "r2",
			GenerationInstance: 2,
			GenerationRevision: 1,
			Disagreements:      []DiagnosticDisagreement{{Path: "canceled.txt", Kind: DisagreementMissingInCatalog, DiskKind: "file"}},
		}
		writer2Done := make(chan error, 1)
		go func() {
			writer2Done <- sampler.retainAndWrite(ctx2, reportPath, rep2, nil, 8, func() bool { return true })
		}()
		time.Sleep(15 * time.Millisecond)
		cancel2()

		select {
		case err := <-writer2Done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected canceled waiter to return context.Canceled, got %v", err)
			}
		case <-time.After(1 * time.Second):
			t.Fatal("canceled waiter blocked on held sampler lock instead of unblocking")
		}

		// Writer 1 (already admitted) completes cleanly when released.
		close(releaseWriter)
		testutil.FailErr(t, "admitted writer1", <-writer1Done)

		// Writer 3 rejected by owner admit() check (simulating detach/Drain after acquire).
		rep3 := &DiagnosticReport{
			ProjectID:          "p3",
			RootID:             "r3",
			GenerationInstance: 3,
			GenerationRevision: 1,
			Disagreements:      []DiagnosticDisagreement{{Path: "drained.txt", Kind: DisagreementMissingInCatalog, DiskKind: "file"}},
		}
		if err := sampler.retainAndWrite(t.Context(), reportPath, rep3, nil, 8, func() bool { return false }); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected admit=false to return context.Canceled, got %v", err)
		}

		raw, err := os.ReadFile(reportPath)
		testutil.FailErr(t, "read published report", err)
		lines := slices.DeleteFunc(splitLines(string(raw)), func(s string) bool { return s == "" })
		if len(lines) != 1 {
			t.Fatalf("expected only admitted writer1 report on disk, got %d lines", len(lines))
		}
		var dec DiagnosticReport
		testutil.FailErr(t, "decode report", json.Unmarshal([]byte(lines[0]), &dec))
		if dec.ProjectID != "p1" {
			t.Fatalf("unexpected report on disk: %+v", dec)
		}
	})
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

func TestDiagnosticFailedOpenCountsAgainstMaxDirsAndMissingStampSuppressesAssertions(t *testing.T) {
	t.Run("failed_opens_charge_max_dirs_at_io_boundary", func(t *testing.T) {
		rootDir := t.TempDir()
		for i := 0; i < 6; i++ {
			dirName := fmt.Sprintf("locked_%02d", i)
			writeIndexFile(t, rootDir, dirName+"/file.txt", "secret")
			dirPath := filepath.Join(rootDir, dirName)
			testutil.FailErr(t, "chmod locked dir", os.Chmod(dirPath, 0o000))
			t.Cleanup(func() { _ = os.Chmod(dirPath, 0o700) })
		}

		// MaxDirs=3 allows 1 open for "." plus at most 2 failed subdirectory open attempts.
		_, _, _, reads, truncated, err := walkDirectSubtree(
			t.Context(),
			rootDir,
			DiagnosticConfig{Enabled: true, Subtree: ".", MaxDirs: 3, MaxEntries: 1024, MaxDepth: 4}.normalized(),
			walkPolicy{},
			map[string]struct{}{},
			map[string]string{},
		)
		testutil.FailErr(t, "walk with failed opens", err)
		if reads.DirAttempts != 3 {
			t.Fatalf("expected DirAttempts bounded at MaxDirs=3, got %+v", reads)
		}
		if reads.DirReads != 1 {
			t.Fatalf("expected only 1 successful DirRead (root '.'), got %+v", reads)
		}
		if !truncated {
			t.Fatal("expected truncated=true when failed opens hit MaxDirs cap")
		}
	})

	t.Run("missing_or_unknown_stamp_suppresses_stale_and_missing_assertions", func(t *testing.T) {
		catalog, root := indexFixture(t)
		writeIndexFile(t, root.Path, "src/present.txt", "ok")
		writeIndexFile(t, root.Path, "src/missing_in_cat.txt", "disk only")
		reader := waitIndex(t, catalog, root)
		store := reader.store
		testutil.FailErr(t, "close reader", reader.Close())

		db, err := openTreeDB(t.Context(), store.file)
		testutil.FailErr(t, "open db", err)
		defer func() { _ = db.Close() }()
		_, err = db.ExecContext(t.Context(), "DELETE FROM nodes WHERE path='src/missing_in_cat.txt'")
		testutil.FailErr(t, "delete node", err)
		_, err = db.ExecContext(t.Context(), `INSERT INTO nodes(
			path,search_path,search_name,parent,name,depth,indexed,agent_metadata,first_listed,is_dir,symlink,regular,hidden,size,mode,modified,refused
		) VALUES('src/ghost_in_cat.txt','src/ghost_in_cat.txt','ghost_in_cat.txt','src','ghost_in_cat.txt',2,1,0,1,0,0,1,0,10,420,1,'')`)
		testutil.FailErr(t, "insert ghost node", err)

		// 1. If walk cannot capture the directory's stamp, it must not populate
		// diskEntries or fullSampledDirs, so neither missing nor extra assertions fire.
		diskEntries, fullSampledDirs, dirStamps, reads, truncated, err := walkDirectSubtreeWithStat(
			t.Context(),
			root.Path,
			DiagnosticConfig{Enabled: true, Subtree: "src", MaxDirs: 8, MaxEntries: 64, MaxDepth: 4}.normalized(),
			walkPolicy{},
			map[string]struct{}{},
			map[string]string{},
			func(*os.File) (os.FileInfo, error) {
				return nil, errors.New("injected stat failure")
			},
		)
		testutil.FailErr(t, "walk with failing stat", err)
		if reads.DirAttempts != 1 || reads.DirReads != 1 || !truncated {
			t.Fatalf("expected 1 attempt, 1 read, truncated=true; got reads=%+v truncated=%v", reads, truncated)
		}
		if len(diskEntries) != 0 || len(fullSampledDirs) != 0 || len(dirStamps) != 0 {
			t.Fatalf("unstamped dir must not authorize entries/sampling: disk=%d full=%d stamps=%d", len(diskEntries), len(fullSampledDirs), len(dirStamps))
		}
		disagreements := compareCatalogAndDisk(
			map[string]catalogNodeRecord{
				"src/present.txt":      {path: "src/present.txt", kind: "file", indexed: true},
				"src/ghost_in_cat.txt": {path: "src/ghost_in_cat.txt", kind: "file", indexed: true},
			},
			diskEntries,
			fullSampledDirs,
			map[string]struct{}{},
			map[string]string{},
			walkPolicy{},
			truncated,
			false,
			"",
		)
		if len(disagreements) != 0 {
			t.Fatalf("missing stamp must not raise disagreements, got %+v", disagreements)
		}

		// 2. verifyGenerationConsistency must reject an unknown/uncaptured beforeStamp.
		store.mu.Lock()
		pinned := store.status
		pinned.Instance = store.instance
		store.mu.Unlock()
		if verifyGenerationConsistency(t.Context(), store, pinned, repochange.CurrentEpoch(root.Path), map[string]DirectoryStamp{"src": {}}) {
			t.Fatal("verifyGenerationConsistency must reject an unknown/uncaptured directory stamp")
		}
	})
}
