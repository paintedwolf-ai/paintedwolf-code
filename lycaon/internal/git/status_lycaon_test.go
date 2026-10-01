package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Project overlay paths participate in source status.
func TestGitManagerStatusReportsProjectOverlayPolicy(t *testing.T) {
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	write := func(rel, content string) {
		abs := filepath.Join(dir, rel)
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(abs, []byte(content), 0o644))
	}
	write("README.md", "hello\n")
	write(settingsoverlay.DirName()+"/blueprints/feature.md", "plan\n")
	git := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	git("add", "README.md")
	git("commit", "-m", "init")
	git("add", settingsoverlay.DirName()+"/blueprints/feature.md")
	git("commit", "-m", "add plan")
	write(settingsoverlay.DirName()+"/blueprints/feature.md", "plan changed\n")

	status, err := NewManager().Status(context.Background(), dir)
	testutil.FailErr(t, "Status", err)
	if len(status.Files) != 1 {
		t.Fatalf("Files = %#v want the changed blueprint", status.Files)
	}
	if status.Files[0].Path != settingsoverlay.DirName()+"/blueprints/feature.md" {
		t.Fatalf("Files[0].Path = %q", status.Files[0].Path)
	}
	if status.UnstagedCount != 1 {
		t.Fatalf("UnstagedCount = %d want 1", status.UnstagedCount)
	}
}
