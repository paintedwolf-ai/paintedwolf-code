package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

type countingLoader struct {
	inner   *git.Manager
	statusN atomic.Int32
	logN    atomic.Int32
}

func (c *countingLoader) Status(ctx context.Context, projectDir string) (*git.GitStatus, error) {
	c.statusN.Add(1)
	return c.inner.Status(ctx, projectDir)
}

func (c *countingLoader) Log(ctx context.Context, projectDir string, opts git.GitLogOpts) ([]git.GitCommit, error) {
	c.logN.Add(1)
	return c.inner.Log(ctx, projectDir, opts)
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mgr := git.NewManager()
	testutil.FailErr(t, "init", mgr.Init(context.Background(), dir))
	path := filepath.Join(dir, "a.txt")
	testutil.FailErr(t, "write", writeFile(path, "one\n"))
	_, commitErr1 := mgr.Commit(context.Background(), dir, git.GitCommitOpts{Message: "init", Paths: []string{"a.txt"}})
	testutil.FailErr(t, "commit", commitErr1)
	return dir
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func TestStatusCache_hitSkipsExec(t *testing.T) {
	dir := initTestRepo(t)
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)

	_, err := cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "first load", err)
	_, err = cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "second load", err)
	if loader.statusN.Load() != 1 {
		t.Fatalf("status execs = %d want 1", loader.statusN.Load())
	}
}

func TestStatusCache_forceAlwaysExecs(t *testing.T) {
	dir := initTestRepo(t)
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)

	_, err := cache.GetOrLoad(context.Background(), dir, true)
	testutil.FailErr(t, "force1", err)
	_, err = cache.GetOrLoad(context.Background(), dir, true)
	testutil.FailErr(t, "force2", err)
	if loader.statusN.Load() != 2 {
		t.Fatalf("status execs = %d want 2", loader.statusN.Load())
	}
}

func TestStatusCache_NotifyInvalidates(t *testing.T) {
	dir := initTestRepo(t)
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)
	t.Cleanup(cache.RegisterRepochangeObserver())

	_, err := cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "load", err)
	repochange.Notify(context.Background(), repochange.Event{ProjectDir: dir, Kind: repochange.HeadMoved})
	_, err = cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "reload", err)
	if loader.statusN.Load() != 2 {
		t.Fatalf("status execs = %d want 2 after invalidate", loader.statusN.Load())
	}
}

func TestStatusCache_WorktreeChangedInvalidates(t *testing.T) {
	dir := initTestRepo(t)
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)
	t.Cleanup(cache.RegisterRepochangeObserver())

	_, err := cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "load", err)
	repochange.Notify(context.Background(), repochange.Event{
		ProjectDir: dir,
		Kind:       repochange.WorktreeChanged,
		Paths:      []string{"a.txt"},
		Source:     repochange.SourceMutation,
	})
	if cache.LastChangeSignal(dir) != repochange.SourceMutation {
		t.Fatalf("LastChangeSignal=%q", cache.LastChangeSignal(dir))
	}
	miss, err := cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "reload after WorktreeChanged", err)
	if loader.statusN.Load() != 2 {
		t.Fatalf("status execs = %d want 2 after WorktreeChanged", loader.statusN.Load())
	}
	if miss.CacheHit {
		t.Fatal("expected miss after WorktreeChanged")
	}
	if miss.ChangeSignal != repochange.SourceMutation {
		t.Fatalf("ChangeSignal=%q", miss.ChangeSignal)
	}
}

func TestStatusCache_singleflightConcurrent(t *testing.T) {
	dir := initTestRepo(t)
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cache.GetOrLoad(context.Background(), dir, false)
			if err != nil {
				t.Errorf("GetOrLoad: %v", err)
			}
		}()
	}
	wg.Wait()
	if loader.statusN.Load() != 1 {
		t.Fatalf("concurrent status execs = %d want 1", loader.statusN.Load())
	}
}

func TestStatusCache_ttlExpiryReloads(t *testing.T) {
	dir := initTestRepo(t)
	loader := &countingLoader{inner: git.NewManager()}
	clock := time.Now()
	cache := git.NewStatusCache(loader, git.WithStatusCacheClock(func() time.Time { return clock }))

	_, err := cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "load", err)
	clock = clock.Add(git.DefaultStatusCacheTTL + time.Millisecond)
	_, err = cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "after ttl", err)
	if loader.statusN.Load() != 2 {
		t.Fatalf("status execs = %d want 2 after TTL", loader.statusN.Load())
	}
}

func TestStatusCache_RecentSubjectsSeparate(t *testing.T) {
	dir := initTestRepo(t)
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)

	_, err := cache.GetOrLoad(context.Background(), dir, false)
	testutil.FailErr(t, "status", err)
	if loader.logN.Load() != 0 {
		t.Fatalf("Status hot path must not Log; logN=%d", loader.logN.Load())
	}
	subjects, err := cache.RecentSubjects(context.Background(), dir, false, 5)
	testutil.FailErr(t, "subjects", err)
	if len(subjects) == 0 {
		t.Fatal("expected at least one subject")
	}
	if loader.logN.Load() != 1 {
		t.Fatalf("logN=%d want 1", loader.logN.Load())
	}
}

func TestManager_CommitNotifiesHeadMoved(t *testing.T) {
	dir := initTestRepo(t)
	var got repochange.Kind
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir == absOr(dir) {
			got = ev.Kind
		}
	})
	mgr := git.NewManager()
	path := filepath.Join(dir, "b.txt")
	testutil.FailErr(t, "write", writeFile(path, "two\n"))
	_, commitErr2 := mgr.Commit(context.Background(), dir, git.GitCommitOpts{Message: "two", Paths: []string{"b.txt"}})
	testutil.FailErr(t, "commit", commitErr2)
	if got != repochange.HeadMoved {
		t.Fatalf("kind = %v want HeadMoved", got)
	}
}

func TestManager_RestoreNotifiesWorktreeChanged(t *testing.T) {
	dir := initTestRepo(t)
	var got repochange.Kind
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if filepath.Clean(ev.ProjectDir) == absOr(dir) {
			got = ev.Kind
		}
	})
	mgr := git.NewManager()
	path := filepath.Join(dir, "a.txt")
	testutil.FailErr(t, "dirty", writeFile(path, "dirty\n"))
	testutil.FailErr(t, "restore", mgr.Restore(context.Background(), dir, git.GitRestoreOpts{
		Paths: []string{"a.txt"}, Source: "HEAD", Staged: true, Worktree: true,
	}))
	if got != repochange.WorktreeChanged {
		t.Fatalf("kind = %v want WorktreeChanged", got)
	}
}

func TestManager_DiscardAllNotifiesWorktreeChanged(t *testing.T) {
	dir := initTestRepo(t)
	var got repochange.Kind
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir == absOr(dir) {
			got = ev.Kind
		}
	})
	mgr := git.NewManager()
	testutil.FailErr(t, "dirty", writeFile(filepath.Join(dir, "a.txt"), "dirty\n"))
	testutil.FailErr(t, "discard", mgr.DiscardAll(context.Background(), dir))
	if got != repochange.WorktreeChanged {
		t.Fatalf("kind = %v want WorktreeChanged", got)
	}
}

func TestManager_StashNotifiesWorktreeChanged(t *testing.T) {
	dir := initTestRepo(t)
	var got repochange.Kind
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir == absOr(dir) {
			got = ev.Kind
		}
	})
	mgr := git.NewManager()
	testutil.FailErr(t, "dirty", writeFile(filepath.Join(dir, "a.txt"), "dirty\n"))
	testutil.FailErr(t, "stash", mgr.Stash(context.Background(), dir, "tmp"))
	if got != repochange.WorktreeChanged {
		t.Fatalf("kind = %v want WorktreeChanged", got)
	}
}

func TestManager_CheckoutNotifiesHeadMoved(t *testing.T) {
	dir := initTestRepo(t)
	mgr := git.NewManager()
	// Create a second branch with raw git (Manager.Checkout only switches).
	cmd := exec.CommandContext(context.Background(), "git", "-C", dir, "branch", "topic")
	cmd.Env = lyexec.LocalGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch topic: %v %s", err, out)
	}

	var got repochange.Kind
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir == absOr(dir) {
			got = ev.Kind
		}
	})
	testutil.FailErr(t, "checkout", mgr.Checkout(context.Background(), dir, "topic"))
	if got != repochange.HeadMoved {
		t.Fatalf("kind = %v want HeadMoved", got)
	}
}

func absOr(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// gatedLoader blocks inside Status until the test releases it, so a revalidate
// can be observed while it is still running.
type gatedLoader struct {
	inner   *git.Manager
	statusN atomic.Int32
	entered chan struct{}
	release chan struct{}
}

type coldGatedLoader struct {
	inner   *git.Manager
	entered chan struct{}
	release chan struct{}
}

type invalidationGatedLoader struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (g *invalidationGatedLoader) Status(ctx context.Context, _ string) (*git.GitStatus, error) {
	switch g.calls.Add(1) {
	case 1:
		return &git.GitStatus{}, nil
	case 2:
		close(g.entered)
		select {
		case <-g.release:
			return &git.GitStatus{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	default:
		return &git.GitStatus{Dirty: true}, nil
	}
}

func (g *invalidationGatedLoader) Log(context.Context, string, git.GitLogOpts) ([]git.GitCommit, error) {
	return nil, nil
}

func (g *coldGatedLoader) Status(ctx context.Context, projectDir string) (*git.GitStatus, error) {
	g.entered <- struct{}{}
	select {
	case <-g.release:
		return g.inner.Status(ctx, projectDir)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *coldGatedLoader) Log(ctx context.Context, projectDir string, opts git.GitLogOpts) ([]git.GitCommit, error) {
	return g.inner.Log(ctx, projectDir, opts)
}

func (g *gatedLoader) Status(ctx context.Context, projectDir string) (*git.GitStatus, error) {
	if n := g.statusN.Add(1); n > 1 {
		g.entered <- struct{}{}
		<-g.release
	}
	return g.inner.Status(ctx, projectDir)
}

func (g *gatedLoader) Log(ctx context.Context, projectDir string, opts git.GitLogOpts) ([]git.GitCommit, error) {
	return g.inner.Log(ctx, projectDir, opts)
}

func TestStatusCache_RevalidateServesStaleWhileReloading(t *testing.T) {
	dir := initTestRepo(t)
	loader := &gatedLoader{
		inner:   git.NewManager(),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	cache := git.NewStatusCache(loader)
	t.Cleanup(cache.RegisterRepochangeObserver())

	first, err := cache.GetOrRevalidate(t.Context(), dir)
	testutil.FailErr(t, "first load", err)
	if first.Status.Dirty {
		t.Fatal("fresh repo reported dirty")
	}

	testutil.FailErr(t, "dirty the tree", writeFile(filepath.Join(dir, "b.txt"), "two\n"))
	repochange.Notify(t.Context(), repochange.Event{ProjectDir: dir, Kind: repochange.WorktreeChanged})

	// The reload is gated, so this call can only return by serving the stale value.
	stale, err := cache.GetOrRevalidate(t.Context(), dir)
	testutil.FailErr(t, "revalidate", err)
	if stale.CacheHit {
		t.Fatal("superseded entry reported as a cache hit")
	}
	if stale.Status.Dirty {
		t.Fatal("revalidate waited for the reload instead of serving the last value")
	}

	select {
	case <-loader.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background revalidate never started")
	}
	close(loader.release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := cache.GetOrRevalidate(t.Context(), dir)
		testutil.FailErr(t, "post-reload read", err)
		if got.Status.Dirty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background revalidate never landed the new status")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStatusCache_RevalidateColdLoadsInline(t *testing.T) {
	dir := initTestRepo(t)
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)

	got, err := cache.GetOrRevalidate(t.Context(), dir)
	testutil.FailErr(t, "cold revalidate", err)
	if got.Status == nil {
		t.Fatal("cold revalidate returned no status")
	}
	if loader.statusN.Load() != 1 {
		t.Fatalf("status execs = %d want 1", loader.statusN.Load())
	}
}

func TestStatusCache_InvalidationDuringLoadRetriesBeforePublishingFresh(t *testing.T) {
	dir := t.TempDir()
	loader := &invalidationGatedLoader{entered: make(chan struct{}), release: make(chan struct{})}
	cache := git.NewStatusCache(loader)

	_, err := cache.GetOrLoad(t.Context(), dir, false)
	testutil.FailErr(t, "prime cache", err)
	cache.InvalidateWithSource(dir, repochange.SourceMutation)

	done := make(chan git.CachedStatus, 1)
	errs := make(chan error, 1)
	go func() {
		got, loadErr := cache.GetOrLoad(t.Context(), dir, false)
		done <- got
		errs <- loadErr
	}()
	select {
	case <-loader.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reload never started")
	}
	cache.InvalidateWithSource(dir, repochange.SourceWatcher)
	close(loader.release)

	select {
	case err := <-errs:
		testutil.FailErr(t, "reload after invalidation", err)
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not finish")
	}
	got := <-done
	if got.Status == nil || !got.Status.Dirty {
		t.Fatalf("status = %+v want retried dirty snapshot", got.Status)
	}
	if calls := loader.calls.Load(); calls != 3 {
		t.Fatalf("status loads = %d want 3", calls)
	}
}

func TestStatusCache_PeekOrRevalidateColdWarmsInBackground(t *testing.T) {
	dir := initTestRepo(t)
	loader := &coldGatedLoader{
		inner:   git.NewManager(),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	cache := git.NewStatusCache(loader)

	cached, found, err := cache.PeekOrRevalidate(t.Context(), dir)
	testutil.FailErr(t, "peek cold status", err)
	if found || cached.Status != nil {
		t.Fatalf("cold peek = %#v, found = %t", cached, found)
	}
	select {
	case <-loader.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cold peek did not start a background status load")
	}
	close(loader.release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		cached, found, err = cache.PeekOrRevalidate(t.Context(), dir)
		testutil.FailErr(t, "peek warmed status", err)
		if found && cached.Status != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background status load did not populate cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
