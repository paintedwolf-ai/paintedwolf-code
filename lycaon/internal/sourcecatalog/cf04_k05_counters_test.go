package sourcecatalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scalefixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

type cf04K05CounterRecord struct {
	FilesTarget            int    `json:"files_target"`
	ExpectedLeafDirs       int    `json:"expected_leaf_dirs"`
	ExpectedTotalDirs      int    `json:"expected_total_dirs"`
	SettledFileCount       int    `json:"settled_file_count"`
	AdmittedDirNodes       int    `json:"admitted_dir_nodes"`
	AdmittedFileNodes      int    `json:"admitted_file_nodes"`
	ChargedDirectories     int    `json:"charged_directories"`
	ChargedObservedEntries int    `json:"charged_observed_entries"`
	PublishedRevisions     int64  `json:"published_revisions"`
	MetaComplete           int    `json:"meta_complete"`
	FaultCount             int    `json:"fault_count"`
	FrontierRemaining      int    `json:"frontier_remaining"`
	PerFileMetadataReads   string `json:"per_file_metadata_reads"`
	ElapsedMSDiagnostic    int64  `json:"elapsed_ms_diagnostic"`
}

func TestCF04K05ScaleWorkCounters(t *testing.T) {
	if os.Getenv("CF04_K05_SCALE") != "1" {
		t.Skip("set CF04_K05_SCALE=1 to run 50k/500k K05 work-counter companion")
	}
	sizes := []int{50_000, 500_000}
	records := make([]cf04K05CounterRecord, 0, len(sizes))

	for _, files := range sizes {
		t.Run(filepath.Base(t.Name())+"_"+itoa(files), func(t *testing.T) {
			treeDir := t.TempDir()
			shape, err := scalefixture.WriteTree(treeDir, files)
			testutil.FailErr(t, "write scalefixture tree", err)

			groupDirs := (shape.Directories + scalefixture.DirectoriesPerGroup - 1) / scalefixture.DirectoriesPerGroup
			expectedTotalDirs := 1 + groupDirs + shape.Directories

			t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
			c := New()
			t.Cleanup(func() { testutil.FailErr(t, "drain catalog", c.Drain(context.Background())) })

			root := Root{ID: "k05-root", Path: treeDir}
			started := time.Now()

			var reader *IndexReader
			var status TreeStatus
			deadline := time.Now().Add(8 * time.Minute)
			for time.Now().Before(deadline) {
				r, st, openErr := c.Trees.OpenIndex(t.Context(), "cf04-k05", root, 500*time.Millisecond)
				testutil.FailErr(t, "open index", openErr)
				if st.State == StateFailed {
					t.Fatalf("index discovery failed: %s", st.Error)
				}
				if r != nil && st.Complete && !st.Refreshing {
					reader = r
					status = st
					break
				}
				if r != nil {
					_ = r.Close()
				}
				time.Sleep(100 * time.Millisecond)
			}
			if reader == nil {
				t.Fatalf("K05 scale tree (%d files) did not settle within deadline; last status=%+v", files, status)
			}
			defer func() { _ = reader.Close() }()

			elapsedMS := time.Since(started).Milliseconds()
			settledCount, err := reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
			testutil.FailErr(t, "read settled file count", err)

			store, err := c.Trees.indexStore(t.Context(), "cf04-k05", root)
			testutil.FailErr(t, "resolve index store", err)
			db, err := openTreeDB(t.Context(), store.file)
			testutil.FailErr(t, "open index sqlite", err)
			defer func() { _ = db.Close() }()

			var rec cf04K05CounterRecord
			rec.FilesTarget = files
			rec.ExpectedLeafDirs = shape.Directories
			rec.ExpectedTotalDirs = expectedTotalDirs
			rec.SettledFileCount = settledCount
			rec.PerFileMetadataReads = "unknown"
			rec.ElapsedMSDiagnostic = elapsedMS

			testutil.FailErr(t, "query meta", db.QueryRowContext(t.Context(), "SELECT revision, complete FROM meta WHERE id=1").Scan(&rec.PublishedRevisions, &rec.MetaComplete))
			testutil.FailErr(t, "query dir nodes", db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM nodes WHERE is_dir=1 AND indexed=1").Scan(&rec.AdmittedDirNodes))
			testutil.FailErr(t, "query file nodes", db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM nodes WHERE regular=1 AND indexed=1").Scan(&rec.AdmittedFileNodes))
			testutil.FailErr(t, "query charges", db.QueryRowContext(t.Context(), "SELECT COUNT(*), COALESCE(SUM(observed),0) FROM charges").Scan(&rec.ChargedDirectories, &rec.ChargedObservedEntries))
			testutil.FailErr(t, "query faults", db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM faults").Scan(&rec.FaultCount))
			testutil.FailErr(t, "query frontier", db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM frontier").Scan(&rec.FrontierRemaining))

			if rec.SettledFileCount != files || rec.AdmittedFileNodes != files {
				t.Fatalf("settled file mismatch: reader=%d nodes=%d want=%d", rec.SettledFileCount, rec.AdmittedFileNodes, files)
			}
			if rec.MetaComplete != 1 || rec.FaultCount != 0 || rec.FrontierRemaining != 0 {
				t.Fatalf("incomplete or faulted scale index: %+v", rec)
			}
			if rec.PublishedRevisions < 1 {
				t.Fatalf("expected >=1 published generation revisions, got %d", rec.PublishedRevisions)
			}
			if rec.AdmittedDirNodes < shape.Directories {
				t.Fatalf("admitted dir nodes %d < expected leaf dirs %d", rec.AdmittedDirNodes, shape.Directories)
			}
			records = append(records, rec)
			raw, _ := json.Marshal(rec)
			t.Logf("CF04_K05_COUNTERS=%s", string(raw))
		})
	}

	if outPath := os.Getenv("CF04_K05_COUNTERS_OUT"); outPath != "" {
		raw, err := json.MarshalIndent(records, "", "  ")
		testutil.FailErr(t, "marshal counter records", err)
		testutil.FailErr(t, "write counter records", os.WriteFile(outPath, append(raw, '\n'), 0o600))
	}
}

func itoa(n int) string {
	if n == 50_000 {
		return "50000"
	}
	if n == 500_000 {
		return "500000"
	}
	return "custom"
}
