package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConcurrentClonesPreserveWinner(t *testing.T) {
	mgr, origin := initWorktreeRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")
	const count = 8
	start := make(chan struct{})
	results := make(chan error, count)
	for range count {
		go func() {
			<-start
			results <- mgr.Clone(t.Context(), origin, dest)
		}()
	}
	close(start)
	succeeded := 0
	for range count {
		err := <-results
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrCloneDestinationExists) {
			t.Errorf("clone returned unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful clones = %d, want 1", succeeded)
	}
	got, err := os.ReadFile(filepath.Join(dest, "a.txt"))
	testutil.FailErr(t, "read winning checkout", err)
	if string(got) != "one\n" {
		t.Fatalf("winning checkout content = %q", got)
	}
	_, err = mgr.Status(t.Context(), dest)
	testutil.FailErr(t, "winning checkout is a repository", err)
}

func TestCloneRejectsEveryExistingDestination(t *testing.T) {
	mgr, origin := initWorktreeRepo(t)
	for _, kind := range []string{"empty_directory", "file", "symlink", "dangling_symlink"} {
		t.Run(kind, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "destination")
			switch kind {
			case "empty_directory":
				testutil.FailErr(t, "create empty directory", os.Mkdir(dest, 0o700))
			case "file":
				testutil.FailErr(t, "create file", os.WriteFile(dest, []byte("keep"), 0o600))
			case "symlink":
				testutil.FailErr(t, "create symlink", os.Symlink(origin, dest))
			case "dangling_symlink":
				testutil.FailErr(t, "create dangling symlink", os.Symlink(dest+"-missing", dest))
			}
			before, err := os.Lstat(dest)
			testutil.FailErr(t, "inspect before clone", err)
			if err := mgr.Clone(t.Context(), origin, dest); !errors.Is(err, ErrCloneDestinationExists) {
				t.Fatalf("clone error = %v, want ErrCloneDestinationExists", err)
			}
			after, err := os.Lstat(dest)
			testutil.FailErr(t, "inspect preserved destination", err)
			if !os.SameFile(before, after) {
				t.Fatal("clone replaced an existing destination")
			}
		})
	}
}

func TestFailedCloneRemovesOnlyClaimedEmptyDestination(t *testing.T) {
	mgr := NewManager()
	parent := t.TempDir()
	dest := filepath.Join(parent, "clone")
	if err := mgr.Clone(t.Context(), filepath.Join(parent, "missing-source"), dest); err == nil {
		t.Fatal("clone from missing source succeeded")
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed clone left empty destination: %v", err)
	}
	claim, err := claimCloneDestination(dest)
	testutil.FailErr(t, "claim destination", err)
	file := filepath.Join(dest, "external.txt")
	testutil.FailErr(t, "concurrent write", os.WriteFile(file, []byte("keep"), 0o600))
	cause := errors.New("clone failed")
	if err := claim.failed(cause); !errors.Is(err, cause) {
		t.Fatalf("cleanup lost original cause: %v", err)
	}
	got, err := os.ReadFile(file)
	testutil.FailErr(t, "read concurrent write", err)
	if string(got) != "keep" {
		t.Fatalf("concurrent content = %q", got)
	}
}

func TestCanceledCloneDoesNotCreateDestination(t *testing.T) {
	mgr, origin := initWorktreeRepo(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dest := filepath.Join(t.TempDir(), "clone")
	if err := mgr.Clone(ctx, origin, dest); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled clone error = %v", err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled clone created destination: %v", err)
	}
}
