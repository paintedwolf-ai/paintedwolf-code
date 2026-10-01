package survey

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func BenchmarkGrepEngine_PrefilterDominatedTree(b *testing.B) {
	dir := b.TempDir()
	for i := 0; i < 200; i++ {
		testutil.FailErr(b, "write prefilter fixture", os.WriteFile(filepath.Join(dir, "n_"+strconv.Itoa(i)+".txt"), []byte("aaaa bbbb cccc dddd\n"), 0o644))
	}
	testutil.FailErr(b, "write matching fixture", os.WriteFile(filepath.Join(dir, "hit.txt"), []byte("FINDME_TOKEN\n"), 0o644))
	boundary := sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{
		ID:    tools.DefaultToolProfileID,
		Tools: map[string]bool{"grep": true, "read": true},
	}})
	tool := &GrepTool{Boundary: boundary}
	ctx := nativefixture.Context(dir)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats := &grepEngineStats{}
		re, err := compileGrepRegex("FINDME_TOKEN", false)
		testutil.FailErr(b, "compile grep regex", err)
		search := &grepSearch{
			ctx: context.Background(), reads: projectpaths.NewReadSession(boundary, ctx), pattern: "FINDME_TOKEN", re: re,
			require: litprefilter.Extract("FINDME_TOKEN", false), stats: stats,
			resp: &grepResponse{Matches: []grepMatch{}}, maxMatches: safecmd.GrepMaxMatches,
		}
		_, targets, err := tool.resolveGrepTargets(context.Background(), ctx, map[string]any{"path": "."})
		testutil.FailErr(b, "resolve grep targets", err)
		for _, target := range targets {
			if err := tool.grepWalkTreeParallel(context.Background(), ctx, target, false, search, sandbox.SurveyOptions{}); err != nil {
				testutil.FailErr(b, "search prefilter tree", err)
			}
		}
		if stats.RegexOrScanRuns.Load() != 1 {
			b.Fatalf("scan runs=%d", stats.RegexOrScanRuns.Load())
		}
	}
}

// BenchmarkGrepEngine_ArtifactRepository uses PW_GREP_BENCH_ROOT for read-only replay.
func BenchmarkGrepEngine_ArtifactRepository(b *testing.B) {
	dir := os.Getenv("PW_GREP_BENCH_ROOT")
	if dir == "" {
		dir = b.TempDir()
		for i := range 1000 {
			if err := os.WriteFile(filepath.Join(dir, "artifact_"+strconv.Itoa(i)+".o"), make([]byte, 4096), 0o644); err != nil {
				b.Fatalf("write artifact: %v", err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "generated.ts"), []byte("RELEASE_BUILD\n"), 0o644); err != nil {
			b.Fatalf("write text: %v", err)
		}
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: tools.DefaultToolProfileID, Tools: map[string]bool{"grep": true, "read": true},
	}})
	tool := &GrepTool{Boundary: boundary}
	filter, err := sandbox.CompileEntryGlob("**/*.{yml,yaml,sh,json,toml,go,rs,ts}")
	if err != nil {
		b.Fatalf("compile glob: %v", err)
	}
	re, err := compileGrepRegex("RELEASE_BUILD", false)
	if err != nil {
		b.Fatalf("compile pattern: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		stats := &grepEngineStats{}
		search := &grepSearch{ctx: ctx, reads: projectpaths.NewReadSession(boundary, nativefixture.Context(dir)), re: re, pattern: "RELEASE_BUILD", pathFilter: filter,
			require: litprefilter.Extract("RELEASE_BUILD", false), stats: stats, maxMatches: 2000, resp: &grepResponse{}}
		_, targets, err := tool.resolveGrepTargets(ctx, nativefixture.Context(dir), map[string]any{"path": "."})
		if err != nil {
			cancel()
			b.Fatalf("resolve targets: %v", err)
		}
		for _, target := range targets {
			if err := tool.grepWalkTree(ctx, nativefixture.Context(dir), target, false, search, sandbox.SurveyOptions{}); err != nil {
				cancel()
				b.Fatalf("search repository: %v (opened=%d)", err, stats.FilesOpened.Load())
			}
		}
		cancel()
		b.ReportMetric(float64(stats.FilesOpened.Load()), "files-opened/op")
		b.ReportMetric(float64(search.binarySkipped), "binary-skipped/op")
		b.ReportMetric(float64(len(search.resp.Matches)), "matches/op")
	}
}

// BenchmarkGrepFileCost splits one searched file's cost into the host read
// through a call's read session, the session's path resolution alone, the
// resolution a one-off read pays, and a plain read of the same bytes. It
// replays PW_GREP_BENCH_ROOT read-only when set.
func BenchmarkGrepFileCost(b *testing.B) {
	dir := os.Getenv("PW_GREP_BENCH_ROOT")
	if dir == "" {
		dir = b.TempDir()
		for i := range 500 {
			testutil.FailErr(b, "write text fixture", os.WriteFile(filepath.Join(dir, "file_"+strconv.Itoa(i)+".go"), make([]byte, 6000), 0o644))
		}
	}
	var files []string
	testutil.FailErr(b, "list fixture", filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || (d.IsDir() && p != dir && d.Name()[0] == '.') {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && len(files) < 3000 {
			files = append(files, p)
		}
		return nil
	}))
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: tools.DefaultToolProfileID, Tools: map[string]bool{"grep": true, "read": true},
	}})
	tctx := nativefixture.Context(dir)
	ctx := context.Background()
	perFile := func(b *testing.B, fn func(string, os.FileInfo)) {
		infos := make([]os.FileInfo, len(files))
		for i, f := range files {
			info, err := os.Lstat(f)
			testutil.FailErr(b, "stat fixture", err)
			infos[i] = info
		}
		b.ResetTimer()
		for range b.N {
			for i, f := range files {
				fn(f, infos[i])
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(files)), "ns/file")
	}
	b.Run("host_read", func(b *testing.B) {
		reads := projectpaths.NewReadSession(boundary, tctx)
		defer reads.Close()
		perFile(b, func(f string, info os.FileInfo) { _, _, _, _ = readGrepFile(ctx, reads, f, info) })
	})
	b.Run("session_resolve", func(b *testing.B) {
		reads := projectpaths.NewReadSession(boundary, tctx)
		defer reads.Close()
		perFile(b, func(f string, _ os.FileInfo) { _, _ = reads.Resolve(ctx, f) })
	})
	b.Run("single_resolve", func(b *testing.B) {
		perFile(b, func(f string, _ os.FileInfo) { _, _ = projectpaths.ResolveRead(ctx, boundary, tctx, f) })
	})
	b.Run("os_read", func(b *testing.B) {
		perFile(b, func(f string, _ os.FileInfo) { _, _ = os.ReadFile(f) })
	})
}
