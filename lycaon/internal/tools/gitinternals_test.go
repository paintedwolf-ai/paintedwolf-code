package tools_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestGitInternalsWriteDenied(t *testing.T) {
	cases := []struct {
		path  string
		class string
	}{
		{".git/hooks/pre-commit", "hooks"},
		{".git/hooks/post-checkout", "hooks"},
		{".git/hooks", "hooks"},
		{".git/hooks/x/y", "hooks"},
		{".git/config", "config"},
		{".GIT/config", "config"},
		{".git/config::$DATA", "config"},
	}
	for _, tc := range cases {
		class, denied := tools.IsGitInternalsWritePath(tc.path)
		if !denied || class != tc.class {
			t.Errorf("IsGitInternalsWritePath(%q) = (%q, %v), want (%q, true)", tc.path, class, denied, tc.class)
		}
	}
}

func TestGitInternalsSubmoduleDepth(t *testing.T) {
	class, denied := tools.IsGitInternalsWritePath("sub/.git/hooks/pre-commit")
	if !denied || class != "hooks" {
		t.Fatalf("submodule hooks: got (%q, %v)", class, denied)
	}
	if _, denied := tools.IsGitInternalsWritePath("mygit/hooks/pre-commit"); denied {
		t.Fatal("mygit/hooks must not match .git component")
	}
}

func TestGitInternalsQualifiedRoot(t *testing.T) {
	class, denied := tools.IsGitInternalsWritePath("@web/.git/config")
	if !denied || class != "config" {
		t.Fatalf("@web/.git/config: got (%q, %v)", class, denied)
	}
}

// Every file git reads as repository configuration can name a filter driver or
// an ssh program, so each location is covered, not only .git/config.
func TestGitInternalsEveryConfigLocation(t *testing.T) {
	for _, path := range []string{
		".git/config",
		".git/config.worktree",
		".git/modules/vendor/config",
		".git/modules/deep/nested/name/config",
		".git/worktrees/feature/config.worktree",
		".git/worktrees/feature/config",
		"sub/.git/modules/x/config",
	} {
		class, denied := tools.IsGitInternalsWritePath(path)
		if !denied || class != "config" {
			t.Errorf("%q = (%q, %v), want (config, true)", path, class, denied)
		}
	}
	// Hooks anywhere below .git, including a submodule's own hooks directory.
	for _, path := range []string{
		".git/hooks/pre-commit",
		".git/modules/vendor/hooks/pre-commit",
		".git/worktrees/feature/hooks/post-checkout",
	} {
		class, denied := tools.IsGitInternalsWritePath(path)
		if !denied || class != "hooks" {
			t.Errorf("%q = (%q, %v), want (hooks, true)", path, class, denied)
		}
	}
}

func TestGitInternalsNarrow(t *testing.T) {
	for _, path := range []string{
		".git/index",
		".git/refs/heads/x",
		".git/HEAD",
		".git/objects/ab/cd",
		"README.md",
		"",
		".",
		"@web",
	} {
		if _, denied := tools.IsGitInternalsWritePath(path); denied {
			t.Errorf("%q must not be denied", path)
		}
	}
}
