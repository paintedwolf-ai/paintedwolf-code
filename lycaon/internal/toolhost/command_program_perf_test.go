package toolhost

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// perfFixture is the command program entry composed exactly as the host
// composes it (toolhost owners over the real registry, executor, SQL
// receipts, supervisor and blob store), rooted at a generated tree.
type perfFixture struct {
	*programtest.Fixture
	catalog *sourcecatalog.Catalog
}

func newPerfFixture(tb testing.TB, catalog *sourcecatalog.Catalog) *perfFixture {
	tb.Helper()
	catalogContract, err := toolcontract.LanguageV1()
	testutil.FailErr(tb, "catalog", err)
	var names []string
	for _, row := range catalogContract.Tools {
		names = append(names, row.Tool)
	}
	f := &perfFixture{catalog: catalog}
	f.Fixture = programtest.New(tb, programtest.Options{Tools: names, Scratch: true,
		Owners: func(boundary *sandbox.Boundary, recorder *invocation.SQLRecorder) (func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error), error) {
			operations := mutation.Operations{Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary}, Move: &native.MoveTool{Boundary: boundary},
				Copy: &native.CopyTool{Boundary: boundary}, Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary},
				Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary}, Extract: &native.ExtractArchiveTool{Boundary: boundary}}
			return programOwnerTools{boundary: boundary, operations: operations, catalog: catalog}.compose(recorder)
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
			return fmt.Sprintf("%06d the needle x marks this line %060d", i, i)
		}
		return fmt.Sprintf("%06d plain filler text without the mark %060d", i, i)
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

// wideTree is exactly traversal_visits entries: the root, 99 directories and
// 99,900 files.
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

// warmCatalog observes dir and builds every file's Bloom filter, as the
// host's index warm-up does.
func warmCatalog(tb testing.TB, catalog *sourcecatalog.Catalog, dir string) {
	tb.Helper()
	snapshot, err := catalog.Observe(context.Background(), "project", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(tb, "observe", err)
	_, err = catalog.Literals.LiteralCandidates(context.Background(), snapshot, sourcecatalog.LiteralQuery{
		RootID: "r1", Base: ".", Require: litprefilter.AnyOf("warm-up"),
		Open: func(entry sourcecatalog.Entry) (io.ReadCloser, error) {
			return os.Open(filepath.Join(dir, filepath.FromSlash(entry.Path)))
		},
	})
	testutil.FailErr(tb, "build blooms", err)
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
