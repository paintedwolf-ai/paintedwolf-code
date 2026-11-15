//go:build darwin || linux

package platform_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

// A worker's first git status through this host's mount compares sizes and times, opening no source
// file, and still reports every kind of change the worker makes.
func TestFirstGitStatusThroughAMountReadsNoContent(t *testing.T) {
	trunk, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve trunk", err)
	aged := time.Now().Add(-time.Hour)
	for i := range 40 {
		name := filepath.Join(trunk, fmt.Sprintf("file-%02d.txt", i))
		testutil.FailErr(t, "seed", os.WriteFile(name, []byte(fmt.Sprintf("body %02d\n", i)), 0o644))
		// Git rereads files written in the second its index is written; these were written long before.
		testutil.FailErr(t, "age", os.Chtimes(name, aged, aged))
	}
	gittest.InitCommit(t, trunk, "initial")
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "privategit.NewManager", err)
	view, _ := mountedHostView(t, trunk, mgr)
	work := view.PrimaryWorkPath
	status := func() []string {
		t.Helper()
		out, code, err := gitexec.Run(t.Context(), work, []string{"status", "--porcelain"}, gitexec.Opts{})
		if err != nil || code != 0 {
			t.Fatalf("git status: exit %d %v\n%s", code, err, out)
		}
		return strings.Fields(strings.TrimSpace(string(out)))
	}

	before := view.Core.Stats().LowerContentReads
	if got := status(); len(got) != 0 {
		t.Fatalf("an untouched tree reported %v", got)
	}
	if reads := view.Core.Stats().LowerContentReads - before; reads != 0 {
		t.Fatalf("the first git status opened %d source files", reads)
	}

	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(work, "file-00.txt"), []byte("a longer body\n"), 0o644))
	// A same-size edit with a later modification time is caught by the time alone.
	sameSize := filepath.Join(work, "file-01.txt")
	testutil.FailErr(t, "same-size edit", os.WriteFile(sameSize, []byte("BODY 01\n"), 0o644))
	later := time.Now().Add(2 * time.Second)
	testutil.FailErr(t, "advance time", os.Chtimes(sameSize, later, later))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(work, "file-02.txt")))
	testutil.FailErr(t, "create", os.WriteFile(filepath.Join(work, "new.txt"), []byte("new\n"), 0o644))
	got := status()
	for _, want := range [][2]string{{"M", "file-00.txt"}, {"M", "file-01.txt"}, {"D", "file-02.txt"}, {"??", "new.txt"}} {
		if i := slices.Index(got, want[1]); i < 1 || got[i-1] != want[0] {
			t.Fatalf("status %v lacks %s %s", got, want[0], want[1])
		}
	}
}
