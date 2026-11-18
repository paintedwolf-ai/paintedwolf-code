//go:build integration

package toolhost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// TestCommandProgramResourceCeilingsAtCapacity runs every capacity bound at and
// one past its limit through the real command entry: 8 MiB pipelines and sort
// spills, 9,999 loop iterations, 250,000 traversal visits, 100x archive
// expansion and 25,000 archive entries, and a 10,000-file indexed grep.
func TestCommandProgramResourceCeilingsAtCapacity(t *testing.T) {
	f := newPerfFixture(t, nil)
	seedResourceCeilingInputs(t, f)
	find250k := "find" + strings.Repeat(" visits/d", 250) + " -maxdepth 1 -name none"
	find250kPast := "find" + strings.Repeat(" visits/d", 250) + " visits/one -maxdepth 1 -name none"

	cases := []resourceCeilingCase{
		{"pipeline-cat-grep-wc-8MiB", "cat big.txt | grep x | wc -l", ""},
		{"sort-spill-8MiB", "sort unsorted.txt | wc -l", ""},
		{"loop-9999-iterations", "n=0; for w in $(cat words.txt); do n=$((n+1)); done; echo $n", ""},
		{"loop-iterations-past-limit", "while :; do :; done", "COMMAND_LIMIT_EXCEEDED"},
		{"builtins-and-redirects-count-visits", "for k in " + strings.Repeat("k ", 200) + "; do { : >/dev/null; true; test -n x; [[ a == a ]]; }; done", ""},
		{"traversal-visits-at-250k", find250k, ""},
		{"traversal-visits-past-250k", find250kPast, "COMMAND_LIMIT_EXCEEDED"},
		{"archive-ratio-at-100x", "zcat at-ratio.gz >/dev/null", ""},
		{"archive-ratio-past-100x", "zcat past-ratio.gz >/dev/null", "COMMAND_LIMIT_EXCEEDED"},
		{"archive-entries-at-25k", "tar -tf entries-at.tar >/dev/null", ""},
		{"archive-entries-past-25k", "tar -tf entries-past.tar >/dev/null", "COMMAND_LIMIT_EXCEEDED"},
	}

	checkResourceCeilings(t, f, cases, func(t *testing.T) {
		t.Run("grep-r-10k-indexed-vs-live", func(t *testing.T) {
			catalog := sourcecatalog.New()
			liveFix := newPerfFixture(t, nil)
			indexedFix := newPerfFixture(t, catalog)
			sourceTree(t, liveFix.Dir)
			sourceTree(t, indexedFix.Dir)
			observable := warmCatalog(t, catalog, indexedFix.Dir)

			var liveOutcome, indexedOutcome command.ProgramOutcome
			testutil.FailErr(t, "unmarshal live grep", json.Unmarshal([]byte(liveFix.run(t, "grep -rl NeedleToken src")), &liveOutcome))
			testutil.FailErr(t, "unmarshal indexed grep", json.Unmarshal([]byte(indexedFix.run(t, "grep -rl NeedleToken src")), &indexedOutcome))
			if liveOutcome.Stdout != "src/p042/f007.go\n" || indexedOutcome.Stdout != "src/p042/f007.go\n" {
				t.Fatalf("grep stdout mismatch: live=%q indexed=%q", liveOutcome.Stdout, indexedOutcome.Stdout)
			}
			liveReceipts := liveFix.receiptCounts(t, liveOutcome.ProgramID)
			indexedReceipts := indexedFix.receiptCounts(t, indexedOutcome.ProgramID)
			// Observable filters must eliminate at least 99% of captures.
			if liveReceipts.pinCount != 10_000 {
				t.Fatalf("live grep pinned files = %d, want 10000", liveReceipts.pinCount)
			}
			if observable && (indexedReceipts.pinCount < 1 || liveReceipts.pinCount < 100*indexedReceipts.pinCount) {
				t.Fatalf("indexed grep pinned %d files vs %d live; want >= 100x fewer", indexedReceipts.pinCount, liveReceipts.pinCount)
			}
			if !observable && indexedReceipts.pinCount != liveReceipts.pinCount {
				t.Fatal("unobservable content changed the live capture count")
			}
			t.Logf("grep-r-10k-indexed-vs-live: live_pinned_files=%d indexed_pinned_files=%d reduction_factor=%.1fx",
				liveReceipts.pinCount, indexedReceipts.pinCount, float64(liveReceipts.pinCount)/float64(indexedReceipts.pinCount))
		})
	})
}

func seedResourceCeilingInputs(t *testing.T, f *perfFixture) {
	t.Helper()
	bigText(t, f.Dir)
	sortInput(t, f.Dir)
	writeLines(t, filepath.Join(f.Dir, "words.txt"), 9_999, func(i int) string { return fmt.Sprint(i) })

	// Seed a 1,000-entry directory (d + 999 files) and a single file (one) so
	// 250 operands of d reach exactly 250,000 traversal_visits.
	visitDir := filepath.Join(f.Dir, "visits", "d")
	testutil.FailErr(t, "mkdir visits/d", os.MkdirAll(visitDir, 0o755))
	for i := range 999 {
		testutil.FailErr(t, "write visit file", os.WriteFile(filepath.Join(visitDir, fmt.Sprintf("f%03d", i)), nil, 0o644))
	}
	testutil.FailErr(t, "write visits/one", os.WriteFile(filepath.Join(f.Dir, "visits", "one"), nil, 0o644))

	// Seed archive_expansion_ratio (100x) and archive_extract_entries (25,000) fixtures.
	testutil.FailErr(t, "write at-ratio.gz", os.WriteFile(filepath.Join(f.Dir, "at-ratio.gz"), paddedPerfGzip(t, 10_000, 100), 0o644))
	testutil.FailErr(t, "write past-ratio.gz", os.WriteFile(filepath.Join(f.Dir, "past-ratio.gz"), paddedPerfGzip(t, 10_001, 100), 0o644))
	writePerfTarHeaders(t, filepath.Join(f.Dir, "entries-at.tar"), 25_000)
	writePerfTarHeaders(t, filepath.Join(f.Dir, "entries-past.tar"), 25_001)

}

func paddedPerfGzip(tb testing.TB, payloadLen, targetGzLen int) []byte {
	tb.Helper()
	payload := bytes.Repeat([]byte("a"), payloadLen)
	build := func(commentLen int) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if commentLen > 0 {
			gz.Comment = strings.Repeat("c", commentLen)
		}
		_, err := gz.Write(payload)
		testutil.FailErr(tb, "gzip write", err)
		testutil.FailErr(tb, "gzip close", gz.Close())
		return buf.Bytes()
	}
	base := build(0)
	if len(base) == targetGzLen {
		return base
	}
	return build(targetGzLen - len(base) - 1)
}

func writePerfTarHeaders(tb testing.TB, path string, count int) {
	tb.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := range count {
		testutil.FailErr(tb, "tar header", tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%05d", i), Mode: 0o644, Size: 0, Typeflag: tar.TypeReg}))
	}
	testutil.FailErr(tb, "tar close", tw.Close())
	testutil.FailErr(tb, "write tar", os.WriteFile(path, buf.Bytes(), 0o644))
}
