package git_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
)

func TestErrNotRepositoryDoesNotMatchProse(t *testing.T) {
	if errors.Is(errors.New("git status failed: fatal: not a git repository"), git.ErrNotRepository) {
		t.Fatal("unexpected not-a-repo for other errors")
	}
	if errors.Is(nil, git.ErrNotRepository) {
		t.Fatal("nil err should be false")
	}
}

func TestRepositoryOperationsReturnTypedNotRepository(t *testing.T) {
	mgr := git.NewManager()
	dir := t.TempDir()
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "status", run: func() error { _, err := mgr.Status(context.Background(), dir); return err }},
		{name: "diff", run: func() error { _, err := mgr.Diff(context.Background(), dir, git.GitDiffOpts{}); return err }},
		{name: "show", run: func() error { _, err := mgr.Show(context.Background(), dir, git.GitShowOpts{}); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, git.ErrNotRepository) {
				t.Fatalf("error = %v, want typed not-repository", err)
			}
		})
	}
}
