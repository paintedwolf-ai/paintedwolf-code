package gitlease

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gittest.Run(t, dir, "init", "-b", "main")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600))
	gittest.CommitAll(t, dir, "one")
	return dir
}

// A clone or ls-remote against a local repository is a live read of refs and
// packs this host may be rewriting, so it takes the same key as the mutating
// side. The test compares keys rather than racing two calls.
func TestCloneSourceTakesTheSameLeaseAsMutatingTheSourceRepository(t *testing.T) {
	src := initRepo(t)
	key, err := repositoryKey(t.Context(), src)
	testutil.FailErr(t, "resolve repository key", err)

	release, err := CloneSource(t.Context(), "file://"+src)
	testutil.FailErr(t, "lease clone source", err)
	defer release()

	// Held, so the token channel is empty and a canceled context is the only
	// ready case in the select — no timing, no flake.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	second, err := acquire(ctx, key)
	if second != nil {
		second()
	}
	if err == nil {
		t.Fatal("mutating the source repository proceeded while a clone held its lease")
	}
}

func TestCloneSourceLeavesRemoteSourcesUnleased(t *testing.T) {
	for _, source := range []string{
		"https://example.invalid/pack.git",
		"git@example.invalid:owner/pack.git",
		"ssh://example.invalid/pack.git",
	} {
		release, err := CloneSource(t.Context(), source)
		testutil.FailErr(t, "lease remote source "+source, err)
		release()
	}
	leases.Lock()
	held := len(leases.byKey)
	leases.Unlock()
	if held != 0 {
		t.Fatalf("remote sources left %d lease keys behind", held)
	}
}

// Cancellation is an error even when the source does not require a repository lease.
func TestCloneSourceSeparatesANonRepositoryFromACanceledCaller(t *testing.T) {
	plain := t.TempDir()
	release, err := CloneSource(t.Context(), plain)
	testutil.FailErr(t, "lease non-repository source", err)
	release()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CloneSource(ctx, initRepo(t)); err == nil {
		t.Fatal("canceled clone-source lease reported success")
	}
}

// One repository, one lease, whichever door the caller came through.
func TestRepositoryAndPathLeasesReleaseTheirKeys(t *testing.T) {
	dir := initRepo(t)
	release, err := Repository(t.Context(), dir)
	testutil.FailErr(t, "repository lease", err)
	pathRelease, err := Path(t.Context(), filepath.Join(dir, "staging"))
	testutil.FailErr(t, "path lease", err)

	release()
	release() // idempotent: a deferred release after an explicit one is normal
	pathRelease()

	leases.Lock()
	held := len(leases.byKey)
	leases.Unlock()
	if held != 0 {
		t.Fatalf("released leases left %d keys behind", held)
	}
}
