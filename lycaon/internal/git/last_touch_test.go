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

// Fixed commit dates distinguish per-path touch order.
func TestGitManagerLastTouchByPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	commitAt := func(rel, content, msg, date string) {
		testutil.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644))
		add := exec.CommandContext(ctx, "git", "-C", dir, "add", rel)
		add.Env = lyexec.LocalGitEnv()
		if out, err := add.CombinedOutput(); err != nil {
			t.Fatalf("git add %s: %v %s", rel, err, out)
		}
		commit := exec.CommandContext(ctx, "git", "-C", dir, "commit", "-m", msg)
		commit.Env = lyexec.LocalGitEnv(
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := commit.CombinedOutput(); err != nil {
			t.Fatalf("git commit %s: %v %s", rel, err, out)
		}
	}

	commitAt("a.txt", "1", "a", "2020-01-01T00:00:00")
	commitAt("b.txt", "1", "b", "2021-01-01T00:00:00")
	commitAt("a.txt", "2", "a2", "2022-01-01T00:00:00") // a.txt touched again, most recent

	m, err := NewManager().LastTouchByPath(ctx, dir)
	testutil.FailErr(t, "LastTouchByPath", err)

	if y := m["a.txt"].Year(); y != 2022 {
		t.Fatalf("a.txt last touch year = %d, want 2022 (most recent wins)", y)
	}
	if y := m["b.txt"].Year(); y != 2021 {
		t.Fatalf("b.txt last touch year = %d, want 2021", y)
	}
	if _, ok := m["never.txt"]; ok {
		t.Fatal("never-committed path should be absent")
	}
}
