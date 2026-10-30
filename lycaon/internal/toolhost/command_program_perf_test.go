package toolhost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/native/textutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// perfFixture is the command program entry composed exactly as the host
// composes it (toolhost owners over the real registry, executor, SQL
// receipts, supervisor and blob store), rooted at a generated tree.
type perfFixture struct {
	*programtest.Fixture
	catalog       *sourcecatalog.Catalog
	spillsCreated atomic.Int64
	spillsActive  atomic.Int64
}

type countingSpillStorage struct {
	base    textutil.SpillStorage
	created *atomic.Int64
	active  *atomic.Int64
}

func (s countingSpillStorage) Create(ctx context.Context) (textutil.SpillRun, error) {
	run, err := s.base.Create(ctx)
	if err != nil {
		return nil, err
	}
	s.created.Add(1)
	s.active.Add(1)
	return &countingSpillRun{SpillRun: run, active: s.active}, nil
}

type countingSpillRun struct {
	textutil.SpillRun
	active  *atomic.Int64
	removed atomic.Bool
}

func (r *countingSpillRun) Remove() error {
	if r.removed.CompareAndSwap(false, true) {
		r.active.Add(-1)
	}
	return r.SpillRun.Remove()
}

func (f *perfFixture) wrapSpills(base textutil.SpillStorage) textutil.SpillStorage {
	return countingSpillStorage{base: base, created: &f.spillsCreated, active: &f.spillsActive}
}

type programReceiptCounts struct {
	traceEvents    int64
	invokedRows    int64
	executionRows  int64
	definitionRows int64
	pinCount       int64
	outputRows     int64
}

func (f *perfFixture) writerRowChanges(tb testing.TB) int64 {
	tb.Helper()
	tx, err := f.Database.BeginTx(tb.Context(), nil)
	testutil.FailErr(tb, "begin writer tx", err)
	defer func() { _ = tx.Rollback() }()
	var changes int64
	testutil.FailErr(tb, "read total_changes", tx.QueryRowContext(tb.Context(), `SELECT total_changes()`).Scan(&changes))
	return changes
}

func (f *perfFixture) receiptCounts(tb testing.TB, programID string) programReceiptCounts {
	tb.Helper()
	var c programReceiptCounts
	testutil.FailErr(tb, "read receipt counts", f.Database.QueryRowContext(tb.Context(), `
		SELECT
			p.trace_events,
			(SELECT count(*) FROM invocation_program_executions WHERE program_id = p.id AND owner_invoked = 1),
			(SELECT count(*) FROM invocation_program_executions WHERE program_id = p.id),
			(SELECT count(*) FROM invocation_program_definitions WHERE program_id = p.id),
			(SELECT COALESCE(SUM(pin_count), 0) FROM invocation_program_revision_pin_segments WHERE program_id = p.id) +
				(SELECT count(*) FROM invocation_program_revision_pins WHERE program_id = p.id),
			(SELECT count(*) FROM invocation_program_outputs WHERE program_id = p.id)
		FROM invocation_programs p WHERE p.id = ?`, programID).Scan(&c.traceEvents, &c.invokedRows, &c.executionRows, &c.definitionRows, &c.pinCount, &c.outputRows))
	return c
}

func newPerfFixture(tb testing.TB, catalog *sourcecatalog.Catalog) *perfFixture {
	tb.Helper()
	catalogContract, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(tb, "catalog", err)
	var names []string
	for _, row := range catalogContract.Tools {
		names = append(names, row.Tool)
	}
	f := &perfFixture{catalog: catalog}
	f.Fixture = programtest.New(tb, programtest.Options{Tools: names,
		Owners: func(ports programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
			operations := mutation.Operations{Write: &native.WriteTool{Boundary: ports.Boundary}, Delete: &native.DeleteTool{Boundary: ports.Boundary}, Move: &native.MoveTool{Boundary: ports.Boundary},
				Copy: &native.CopyTool{Boundary: ports.Boundary}, Mkdir: &native.MkdirTool{Boundary: ports.Boundary}, Chmod: &native.ChmodTool{Boundary: ports.Boundary},
				Chown: &native.ChownTool{Boundary: ports.Boundary}, Times: &native.TimesTool{Boundary: ports.Boundary}, Extract: &native.ExtractArchiveTool{Boundary: ports.Boundary}}
			owners, err := programOwnerTools{boundary: ports.Boundary, operations: operations, catalog: catalog, spills: f.wrapSpills}.compose(ports.Recorder)
			testutil.FailErr(tb, "compose program owners", err)
			return owners
		}})
	return f
}

// run executes one program and fails on a refused or incomplete outcome.
func (f *perfFixture) run(tb testing.TB, source string) string {
	tb.Helper()
	out, err := f.Call(tb, context.Background(), source)
	if err != nil {
		tb.Fatalf("%s: %v\n%s", source, err, out)
	}
	return out
}

// Datasets. Generated deterministically from fixed seeds so every run sees
// identical bytes; sizes sit at the catalog ceilings they exercise.
func writeLines(tb testing.TB, path string, lines int, line func(i int) string) int64 {
	tb.Helper()
	testutil.FailErr(tb, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	var b strings.Builder
	for i := range lines {
		b.WriteString(line(i))
		b.WriteByte('\n')
	}
	testutil.FailErr(tb, "write "+path, os.WriteFile(path, []byte(b.String()), 0o644))
	return int64(b.Len())
}

// bigText is just under the native read cap (8 MiB): 1 in 64 lines holds x.
func bigText(tb testing.TB, dir string) {
	writeLines(tb, filepath.Join(dir, "big.txt"), 120_000, func(i int) string {
		if i%64 == 0 {
			return fmt.Sprintf("%06d the needle x marks this line %026d", i, i)
		}
		return fmt.Sprintf("%06d plain filler text without the mark %026d", i, i)
	})
}

// sortInput is random keys just under the read cap, past sort_memory_bytes.
func sortInput(tb testing.TB, dir string) {
	rng := rand.New(rand.NewPCG(7, 13))
	writeLines(tb, filepath.Join(dir, "unsorted.txt"), 120_000, func(int) string {
		return fmt.Sprintf("%016x %050d", rng.Uint64(), rng.Uint32())
	})
}

// sourceTree is a 10,000-file repository shape: 100 packages of 100 files.
func sourceTree(tb testing.TB, dir string) {
	for p := range 100 {
		for f := range 100 {
			body := fmt.Sprintf("package p%d\n\nfunc F%d() int { return %d }\n", p, f, p*100+f)
			if p == 42 && f == 7 {
				body += "// NeedleToken lives here\n"
			}
			name := filepath.Join(dir, "src", fmt.Sprintf("p%03d", p), fmt.Sprintf("f%03d.go", f))
			testutil.FailErr(tb, "mkdir", os.MkdirAll(filepath.Dir(name), 0o755))
			testutil.FailErr(tb, "write", os.WriteFile(name, []byte(body), 0o644))
		}
	}
}

// wideTree is 100,000 entries: the root, 99 directories and 99,900 files.
func wideTree(tb testing.TB, dir string) {
	root := filepath.Join(dir, "wide")
	for d := range 99 {
		sub := filepath.Join(root, fmt.Sprintf("d%02d", d))
		testutil.FailErr(tb, "mkdir", os.MkdirAll(sub, 0o755))
		for f := range 1009 {
			if d*1009+f >= 99_900 {
				break
			}
			testutil.FailErr(tb, "touch", os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%04d", f)), nil, 0o644))
		}
	}
}

// BenchmarkCommandProgram measures programs through the command entry. Each
// operation is one complete program call: parse, admission, expansion,
// dispatch, owner I/O, receipts and settlement.
func BenchmarkCommandProgram(b *testing.B) {
	f := newPerfFixture(b, nil)
	writeLines(b, filepath.Join(f.Dir, "small.txt"), 100, func(i int) string { return fmt.Sprintf("line %d", i) })
	bigText(b, f.Dir)
	sortInput(b, f.Dir)
	writeLines(b, filepath.Join(f.Dir, "words.txt"), 9_999, func(i int) string { return fmt.Sprint(i) })
	cases := []struct{ name, program string }{
		{"noop", ":"},
		{"fifty-statements", strings.Repeat(": ; ", 49) + ":"},
		{"native-child-wc", "wc -l small.txt"},
		{"pipeline-cat-grep-wc-8MiB", "cat big.txt | grep x | wc -l"},
		{"sort-spill-8MiB", "sort unsorted.txt | wc -l"},
		{"loop-9999-iterations", "n=0; for w in $(cat words.txt); do n=$((n+1)); done; echo $n"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			f.run(b, c.program)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				f.run(b, c.program)
			}
		})
	}
}

// BenchmarkStructuredWc is the structured tool the native wc child replaces,
// on the same file, for the per-child dispatch comparison.
func BenchmarkStructuredWc(b *testing.B) {
	f := newPerfFixture(b, nil)
	writeLines(b, filepath.Join(f.Dir, "small.txt"), 100, func(i int) string { return fmt.Sprintf("line %d", i) })
	tool := newStructuredWc(f)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := tool.run(b, []string{"small.txt"}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCommandSearch measures grep over a 10,000-file tree, live and with
// the source catalog's Bloom index warmed, and find over traversal_visits
// entries.
func BenchmarkCommandSearch(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("grep-r-10k-indexed=%v", indexed), func(b *testing.B) {
			var catalog *sourcecatalog.Catalog
			if indexed {
				catalog = sourcecatalog.New()
			}
			f := newPerfFixture(b, catalog)
			sourceTree(b, f.Dir)
			if indexed {
				warmCatalog(b, catalog, f.Dir)
			}
			f.run(b, "grep -rl NeedleToken src")
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				f.run(b, "grep -rl NeedleToken src")
			}
		})
	}
	b.Run("find-100k-entries", func(b *testing.B) {
		f := newPerfFixture(b, nil)
		wideTree(b, f.Dir)
		f.run(b, "find wide -type f | wc -l")
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			f.run(b, "find wide -type f | wc -l")
		}
	})
}

// warmCatalog primes captured content before command execution.
func warmCatalog(tb testing.TB, catalog *sourcecatalog.Catalog, dir string) bool {
	tb.Helper()
	snapshot, err := catalog.Observe(tb.Context(), "project", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(tb, "observe fixture", err)
	observable := true
	for _, entry := range snapshot.Entries {
		if entry.IsDir || entry.IsSymlink {
			continue
		}
		file, err := os.Open(filepath.Join(dir, filepath.FromSlash(entry.Path)))
		testutil.FailErr(tb, "open fixture content", err)
		info, err := file.Stat()
		testutil.FailErr(tb, "stat fixture content", err)
		version := fspath.ObserveFile(info)
		observable = observable && version.Known && version.Links > 0
		observation := catalog.Literals.BeginRead(dir, entry, version)
		_, err = io.Copy(io.Discard, observation.Wrap(tb.Context(), file))
		testutil.FailErr(tb, "observe fixture stream", err)
		testutil.FailErr(tb, "close fixture stream", file.Close())
		observation.Commit()
		observation.Close()
	}
	return observable
}

// structuredWc runs the structured wc tool in the fixture's root.
type structuredWc struct {
	tool *survey.WcTool
	tc   tools.ToolContext
}

func newStructuredWc(f *perfFixture) structuredWc {
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: f.Dir, IsPrimary: true}}, ActiveRootID: "r1", SourceWorkspaceKind: api.SourceWorkspaceKindProject}}
	tc.Identity.Agent, tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = "fixture", testdbseed.DefaultProjectID, f.Session, "structured-wc"
	return structuredWc{tool: &survey.WcTool{Boundary: f.Boundary}, tc: tc}
}

func (s structuredWc) run(tb testing.TB, paths []string) (string, error) {
	args := []any{}
	for _, p := range paths {
		args = append(args, p)
	}
	return s.tool.Run(context.Background(), map[string]any{"paths": args}, s.tc)
}

func currentMaxRSSBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	// Darwin reports Maxrss in bytes; Linux reports KiB.
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
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

func seedResourceCeilingInputs(t *testing.T, f *perfFixture) {
	t.Helper()
	writeLines(t, filepath.Join(f.Dir, "small.txt"), 100, func(i int) string { return fmt.Sprintf("line %d", i) })
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

// TestCommandProgramResourceCeilings exercises the declared resource limits
// through the real command entry, verifying that memory, spill files, process
// count and goroutines stay bounded, limit refusals fire at 1 past each bound,
// and cleanup leaves nothing behind. Elapsed time is logged for manual
// inspection; automated gates use deterministic resource counts.
func TestCommandProgramResourceCeilings(t *testing.T) {
	f := newPerfFixture(t, nil)
	seedResourceCeilingInputs(t, f)
	find250k := "find" + strings.Repeat(" visits/d", 250) + " -maxdepth 1 -name none"
	find250kPast := "find" + strings.Repeat(" visits/d", 250) + " visits/one -maxdepth 1 -name none"

	cases := []struct {
		name      string
		program   string
		wantError string
	}{
		{"noop", ":", ""},
		{"fifty-statements", strings.Repeat(": ; ", 49) + ":", ""},
		{"native-child-wc", "wc -l small.txt", ""},
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

	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()
	var baseMem runtime.MemStats
	runtime.ReadMemStats(&baseMem)
	peakRSS := currentMaxRSSBytes()
	peakGoroutines := baseGoroutines
	var peakHeapInuse uint64

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.spillsCreated.Store(0)
			beforeChanges := f.writerRowChanges(t)
			start := time.Now()
			out, err := f.Call(t, context.Background(), c.program)
			elapsed := time.Since(start)
			writerChanges := f.writerRowChanges(t) - beforeChanges
			spillsCreated := f.spillsCreated.Load()
			spillsActive := f.spillsActive.Load()
			if spillsActive != 0 {
				t.Fatalf("%s leaked %d active sort spill runs", c.name, spillsActive)
			}
			var outcome command.ProgramOutcome
			if len(out) > 0 && out[0] == '{' {
				testutil.FailErr(t, "unmarshal outcome", json.Unmarshal([]byte(out), &outcome))
			}
			var receipts programReceiptCounts
			if outcome.ProgramID != "" {
				receipts = f.receiptCounts(t, outcome.ProgramID)
			}
			if c.wantError == "" {
				if err != nil || outcome.Refusal != nil {
					t.Fatalf("%s failed: err=%v out=%s", c.name, err, out)
				}
				if receipts.invokedRows != int64(outcome.ExecutedChildren) {
					t.Fatalf("%s invoked rows = %d, want executed_children = %d", c.name, receipts.invokedRows, outcome.ExecutedChildren)
				}
				switch c.name {
				case "noop", "fifty-statements", "builtins-and-redirects-count-visits":
					if outcome.ExecutedChildren != 0 || receipts.executionRows != 0 {
						t.Fatalf("%s charged child executions for builtins/redirects: executed_children=%d rows=%d", c.name, outcome.ExecutedChildren, receipts.executionRows)
					}
				case "loop-9999-iterations":
					// The 9,999-iteration loop dispatches 2 real children (cat words.txt and echo $n)
					// plus 1 substitution frame; loop iterations and arithmetic issue O(1) receipt
					// statements per child, not O(n).
					if outcome.ExecutedChildren != 2 || receipts.executionRows != 3 || receipts.definitionRows != 3 || receipts.pinCount != 1 {
						t.Fatalf("%s unexpected child/receipt counts: children=%d exec_rows=%d def_rows=%d pins=%d",
							c.name, outcome.ExecutedChildren, receipts.executionRows, receipts.definitionRows, receipts.pinCount)
					}
					if receipts.traceEvents > 35 || writerChanges > 60 {
						t.Fatalf("%s receipt work scaled with loop iterations: trace_events=%d writer_row_changes=%d",
							c.name, receipts.traceEvents, writerChanges)
					}
				case "sort-spill-8MiB":
					if spillsCreated < 1 || spillsCreated > 8 {
						t.Fatalf("%s expected bounded spill files (1..8), got %d", c.name, spillsCreated)
					}
				}
				if c.name != "sort-spill-8MiB" && spillsCreated != 0 {
					t.Fatalf("%s unexpectedly created %d sort spill files", c.name, spillsCreated)
				}
			} else if !strings.Contains(out, c.wantError) && (err == nil || !strings.Contains(err.Error(), c.wantError)) {
				t.Fatalf("%s: want %s, got err=%v out=%s", c.name, c.wantError, err, out)
			}
			if active := f.Background.ActiveJobs(f.Session); len(active) != 0 {
				t.Fatalf("%s leaked %d active background jobs", c.name, len(active))
			}
			for _, sub := range []string{"command-spills", ".painted-wolf-sort-spills"} {
				spillsDir := filepath.Join(f.ScratchDir, sub)
				if entries, readErr := os.ReadDir(spillsDir); readErr == nil && len(entries) != 0 {
					t.Fatalf("%s left %d spill entries in %s", c.name, len(entries), spillsDir)
				}
			}
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peakHeapInuse {
				peakHeapInuse = ms.HeapInuse
			}
			if g := runtime.NumGoroutine(); g > peakGoroutines {
				peakGoroutines = g
			}
			if rss := currentMaxRSSBytes(); rss > peakRSS {
				peakRSS = rss
			}
			t.Logf("%s: elapsed=%v children=%d exec_rows=%d trace_events=%d writer_changes=%d spills_created=%d heap_inuse=%.2fMiB goroutines=%d peak_rss=%.2fMiB",
				c.name, elapsed, outcome.ExecutedChildren, receipts.executionRows, receipts.traceEvents, writerChanges, spillsCreated, float64(ms.HeapInuse)/(1<<20), runtime.NumGoroutine(), float64(peakRSS)/(1<<20))
		})
	}

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

	runtime.GC()
	finalGoroutines := runtime.NumGoroutine()
	var finalMem runtime.MemStats
	runtime.ReadMemStats(&finalMem)
	if rss := currentMaxRSSBytes(); rss > peakRSS {
		peakRSS = rss
	}

	var unsettled int
	testutil.FailErr(t, "check unsettled executions", f.Database.QueryRowContext(t.Context(),
		`SELECT (SELECT count(*) FROM invocation_programs WHERE state IN ('running','waiting')) + (SELECT count(*) FROM invocation_program_executions WHERE state IN ('prepared','waiting','running'))`).Scan(&unsettled))
	if unsettled != 0 {
		t.Fatalf("leaked %d unsettled program/execution receipts", unsettled)
	}
	if finalGoroutines > baseGoroutines+8 {
		t.Fatalf("goroutine leak after ceiling runs: base=%d final=%d", baseGoroutines, finalGoroutines)
	}
	const maxPeakHeapInuse = 512 << 20 // 512 MiB ceiling
	if peakHeapInuse > maxPeakHeapInuse {
		t.Fatalf("peak heap in-use %d bytes exceeded %d bytes", peakHeapInuse, maxPeakHeapInuse)
	}
	t.Logf("resource ceiling summary: base_goroutines=%d peak_goroutines=%d final_goroutines=%d peak_heap_inuse=%.2fMiB final_heap_inuse=%.2fMiB peak_rss=%.2fMiB",
		baseGoroutines, peakGoroutines, finalGoroutines, float64(peakHeapInuse)/(1<<20), float64(finalMem.HeapInuse)/(1<<20), float64(peakRSS)/(1<<20))
}
