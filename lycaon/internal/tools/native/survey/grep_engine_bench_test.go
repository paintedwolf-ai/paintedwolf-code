package survey

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/toolprofiles"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

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
		ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"grep": true, "read": true},
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
