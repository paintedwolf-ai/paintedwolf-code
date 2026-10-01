package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGitManagerCloneCopiesRepo(t *testing.T) {
	mgr := NewManager()

	origin := filepath.Join(t.TempDir(), "origin")
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	testutil.FailErr(t, "mkdir origin", os.MkdirAll(origin, 0o755))
	runGit(origin, "init")
	runGit(origin, "-c", "user.email=t@example.com", "-c", "user.name=Test", "commit", "--allow-empty", "-m", "root")
	testutil.FailErr(t, "write README", os.WriteFile(filepath.Join(origin, "README.md"), []byte("hi"), 0o644))
	runGit(origin, "add", ".")
	runGit(origin, "-c", "user.email=t@example.com", "-c", "user.name=Test", "commit", "-m", "readme")

	dest := filepath.Join(t.TempDir(), "clone")
	if err := mgr.Clone(context.Background(), origin, dest); err != nil {
		t.Skipf("clone: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "README.md")); err != nil || string(got) != "hi" {
		t.Fatalf("cloned README.md = %q err=%v", got, err)
	}
	if _, err := mgr.Status(context.Background(), dest); err != nil {
		t.Fatalf("cloned dir is not a repo: %v", err)
	}
}
