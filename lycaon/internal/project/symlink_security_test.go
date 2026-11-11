package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// symlinkOrSkip creates target -> link; if the filesystem disallows symlinks
// (rare in tests, common in restricted CI sandboxes), the test is skipped.
func symlinkOrSkip(t *testing.T, target, link string) error {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrInvalid) {
			t.Skipf("filesystem disallows symlinks: %v", err)
		}
		return err
	}
	return nil
}

func TestResolveExistingDirFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests are unreliable on Windows CI")
	}
	target := t.TempDir()
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "shortcut")
	if err := symlinkOrSkip(t, target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := ResolveExistingDir(link)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	wantResolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("eval symlinks of target: %v", err)
	}
	if got != wantResolvedTarget {
		t.Fatalf("resolved = %q, want symlink target %q (link path %q must not survive)", got, wantResolvedTarget, link)
	}
}

// TestResolveExistingDirCanonicalizesSymlinkToProtectedPrefix ensures a symlink
// to a protected directory (such as ~/.ssh) resolves to its canonical target
// path without attach-time refusal.
func TestResolveExistingDirCanonicalizesSymlinkToProtectedPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests are unreliable on Windows CI")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}

	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "ssh-link")
	if err := symlinkOrSkip(t, ssh, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	resolved, err := ResolveExistingDir(link)
	if err != nil {
		t.Fatalf("resolve symlink to .ssh: %v", err)
	}
	wantSSH, err := filepath.EvalSymlinks(ssh)
	if err != nil {
		t.Fatalf("eval symlinks of .ssh: %v", err)
	}
	if resolved != wantSSH {
		t.Fatalf("resolved = %q, want canonical target %q", resolved, wantSSH)
	}
}
