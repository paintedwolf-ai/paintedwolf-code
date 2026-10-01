package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

// optionInjectionRepo builds a one-commit repository for the injection cases.
func optionInjectionRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one\n"), 0o600))
	run("add", "-A")
	run("-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-m", "seed")
	return dir
}

// Option-shaped refs are rejected and isolated from command options.
func TestRefArgsCannotBecomeOptions(t *testing.T) {
	dir := optionInjectionRepo(t)
	mgr := NewManager()
	sentinel := filepath.Join(t.TempDir(), "written-by-git")
	payload := "--output=" + sentinel

	assertNotWritten := func(t *testing.T, what string) {
		t.Helper()
		if _, err := os.Stat(sentinel); err == nil {
			t.Fatalf("%s: git wrote %s — a ref reached an option position", what, sentinel)
		}
	}

	t.Run("show ref", func(t *testing.T) {
		_, err := mgr.Show(t.Context(), dir, GitShowOpts{Ref: payload})
		if err == nil {
			t.Fatal("Show accepted an option-shaped ref")
		}
		assertNotWritten(t, "Show")
	})

	t.Run("show blob ref", func(t *testing.T) {
		_, err := mgr.Show(t.Context(), dir, GitShowOpts{Ref: payload, Path: "tracked.txt"})
		if err == nil {
			t.Fatal("Show accepted an option-shaped ref for a blob")
		}
		assertNotWritten(t, "Show blob")
	})

	t.Run("rev-parse ref", func(t *testing.T) {
		_, err := mgr.RevParse(t.Context(), dir, []string{payload})
		if err == nil {
			t.Fatal("RevParse accepted an option-shaped ref")
		}
		assertNotWritten(t, "RevParse")
	})

	t.Run("checkout branch", func(t *testing.T) {
		err := mgr.Checkout(t.Context(), dir, payload)
		if err == nil {
			t.Fatal("Checkout accepted an option-shaped branch")
		}
		assertNotWritten(t, "Checkout")
	})

	t.Run("diff base ref", func(t *testing.T) {
		_, err := mgr.DiffFromRef(t.Context(), dir, payload, "tracked.txt")
		if err == nil {
			t.Fatal("DiffFromRef accepted an option-shaped base ref")
		}
		assertNotWritten(t, "DiffFromRef")
	})
}

// Branch selection treats filenames as refs, preserving working changes.
func TestCheckoutDoesNotRestorePaths(t *testing.T) {
	dir := optionInjectionRepo(t)
	mgr := NewManager()

	tracked := filepath.Join(dir, "tracked.txt")
	testutil.FailErr(t, "dirty the file", os.WriteFile(tracked, []byte("one\nlocal edit\n"), 0o600))

	err := mgr.Checkout(t.Context(), dir, "tracked.txt")
	if err == nil {
		t.Fatal("Checkout accepted a path as a branch")
	}

	body, readErr := os.ReadFile(tracked)
	testutil.FailErr(t, "read back", readErr)
	if !strings.Contains(string(body), "local edit") {
		t.Fatal("Checkout discarded uncommitted work for a path argument")
	}
}
