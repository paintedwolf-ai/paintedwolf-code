package board_test

import (
	"context"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/pkg/api"
)

type countingStatusLoader struct {
	inner   *git.Manager
	statusN atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (c *countingStatusLoader) Status(ctx context.Context, projectDir string) (*git.GitStatus, error) {
	c.statusN.Add(1)
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.inner.Status(ctx, projectDir)
}

func (c *countingStatusLoader) Log(ctx context.Context, projectDir string, opts git.GitLogOpts) ([]git.GitCommit, error) {
	return c.inner.Log(ctx, projectDir, opts)
}

// A cold board never waits for porcelain; the explicit Git surface joins the
// same background fill instead of launching a duplicate process.
func TestColdBoardWarmsSharedGitStatusWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644))
	gittest.InitCommit(t, dir, "init")
	loader := &countingStatusLoader{
		inner:   git.NewManager(),
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	cache := git.NewStatusCache(loader)
	b := &board.SnapshotBuilder{
		StatusCache: cache,
		RepoSets:    git.NewRepoSetCache(git.DefaultStatusCacheTTL),
		Git:         loader.inner,
		Repo:        repotest.NewProvider(t),
	}

	buildDone := make(chan error, 1)
	go func() {
		_, err := b.Build(context.Background(), "proj", dir, "", api.BoardDetailLevelStatus, nil)
		buildDone <- err
	}()
	select {
	case err := <-buildDone:
		testutil.FailErr(t, "board build", err)
	case <-time.After(time.Second):
		t.Fatal("cold board blocked on git status")
	}
	select {
	case <-loader.started:
	case <-time.After(time.Second):
		t.Fatal("cold board did not start background git warm")
	}

	close(loader.release)
	_, err := cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "git status warm", err)
	if loader.statusN.Load() != 1 {
		t.Fatalf("board-then-git statusN = %d want 1 (TTL bridge)", loader.statusN.Load())
	}
}
