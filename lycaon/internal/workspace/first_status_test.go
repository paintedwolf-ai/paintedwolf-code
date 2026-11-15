//go:build darwin || linux

package workspace_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/workspace"
)

// A materialized branch's first git status trusts the copies' sizes and times. Every tracked file is
// made unreadable first, so any content read would surface as an error or a change.
func TestFirstGitStatusOfAMaterializedBranchReadsNoContent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	source := t.TempDir()
	aged := time.Now().Add(-time.Hour)
	var files []string
	for i := range 40 {
		name := fmt.Sprintf("file-%02d.txt", i)
		files = append(files, name)
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(source, name), []byte(fmt.Sprintf("body %02d\n", i)), 0o644))
		// Git rereads files written in the second its index is written; these were written long before.
		testutil.FailErr(t, "age", os.Chtimes(filepath.Join(source, name), aged, aged))
	}
	gittest.InitCommit(t, source, "initial")
	state, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve state", err)
	mgr := workspace.NewManager(enginepaths.WorkerBranchesRootUnder(state))
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), source, "job-first-status")
	testutil.FailErr(t, "create branch", err)
	t.Cleanup(func() { _ = mgr.DestroyWorkerWorkspace(context.Background(), binding) })

	for _, name := range files {
		testutil.FailErr(t, "lock "+name, os.Chmod(filepath.Join(binding.Root, name), 0))
	}
	t.Cleanup(func() {
		for _, name := range files {
			_ = os.Chmod(filepath.Join(binding.Root, name), 0o644)
		}
	})
	out, code, err := gitexec.Run(t.Context(), binding.Root, []string{"status", "--porcelain"}, gitexec.Opts{})
	if err != nil || code != 0 || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("first git status read file content: exit %d %v\n%s", code, err, out)
	}
}
