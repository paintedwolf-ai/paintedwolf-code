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

// TestValidateOpenPathBlocksResolvedSymlinkToDeniedPrefix ensures a symlink under
// an allowed directory cannot bypass the deny list when the target is resolved.
func TestValidateOpenPathBlocksResolvedSymlinkToDeniedPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests are unreliable on Windows CI")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	ssh := filepath.Join(home, ".ssh")
	if st, err := os.Stat(ssh); err != nil || !st.IsDir() {
		t.Skip("~/.ssh not present as directory")
	}

	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "ssh-link")
	if err := symlinkOrSkip(t, ssh, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	resolved, err := ResolveExistingDir(link)
	if err != nil {
		// confine.AttachedWriteRootRefused refuses secret-store targets before
		// OpenPolicy runs, so a resolve failure passes.
		if !errors.Is(err, ErrRootRefused) {
			t.Fatalf("resolve: %v (want ErrRootRefused or open-policy deny)", err)
		}
		return
	}
	if err := DefaultOpenPolicy().ValidateOpenPath(resolved); err == nil {
		t.Fatalf("policy unexpectedly allowed resolved=%q via link=%q", resolved, link)
	}
}
