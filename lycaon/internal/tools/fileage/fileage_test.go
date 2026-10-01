package fileage

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGit struct {
	mu        sync.Mutex
	files     map[string]time.Time
	head      string
	err       error
	scanCalls int
	headCalls int
}

func (f *fakeGit) LastTouchByPath(context.Context, string) (map[string]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanCalls++
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]time.Time, len(f.files))
	for k, v := range f.files {
		out[k] = v
	}
	return out, nil
}

func (f *fakeGit) HeadSHA(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headCalls++
	return f.head, nil
}

func (f *fakeGit) set(files map[string]time.Time, head string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files, f.head = files, head
}

func (f *fakeGit) scans() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scanCalls
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func fourFiles() map[string]time.Time {
	return map[string]time.Time{
		"oldest.txt": day("2020-01-01"),
		"old.txt":    day("2021-01-01"),
		"new.txt":    day("2022-01-01"),
		"newest.txt": day("2023-01-01"),
	}
}

func TestPositionPercentile(t *testing.T) {
	p := New(&fakeGit{files: fourFiles(), head: "sha1"})
	ctx := context.Background()
	p.Prewarm(ctx, "/repo")

	// oldest: 3 of 4 files are more recent → older than 75%.
	pos, ok := p.Position(ctx, "/repo", "oldest.txt")
	if !ok {
		t.Fatal("expected position for oldest.txt")
	}
	if pos.OlderThanPct != 75 {
		t.Fatalf("oldest OlderThanPct = %d, want 75", pos.OlderThanPct)
	}
	if pos.TrackedFiles != 4 {
		t.Fatalf("TrackedFiles = %d, want 4", pos.TrackedFiles)
	}
	if !strings.Contains(pos.Summary(), "older than ~75% of tracked files") {
		t.Fatalf("summary = %q", pos.Summary())
	}

	// newest: nothing is more recent → 0%.
	newest, _ := p.Position(ctx, "/repo", "newest.txt")
	if newest.OlderThanPct != 0 {
		t.Fatalf("newest OlderThanPct = %d, want 0", newest.OlderThanPct)
	}
}

func TestPositionUntrackedReturnsFalse(t *testing.T) {
	p := New(&fakeGit{files: fourFiles(), head: "sha1"})
	p.Prewarm(context.Background(), "/repo")
	if _, ok := p.Position(context.Background(), "/repo", "unknown.txt"); ok {
		t.Fatal("untracked path should return false")
	}
}

func TestPositionSingleFileDistributionIsUnplaceable(t *testing.T) {
	p := New(&fakeGit{files: map[string]time.Time{"solo.txt": day("2020-01-01")}, head: "sha1"})
	p.Prewarm(context.Background(), "/repo")
	if _, ok := p.Position(context.Background(), "/repo", "solo.txt"); ok {
		t.Fatal("a one-file repo cannot situate a file")
	}
}

// The first read returns before the background scan produces a position.
func TestPositionFirstReadDoesNotBlock(t *testing.T) {
	p := New(&fakeGit{files: fourFiles(), head: "sha1"})
	ctx := context.Background()
	if _, ok := p.Position(ctx, "/repo", "old.txt"); ok {
		t.Fatal("first read must not wait on the scan; expected no fact yet")
	}
	if !eventually(func() bool { _, ok := p.Position(ctx, "/repo", "old.txt"); return ok }) {
		t.Fatal("background build never became ready")
	}
}

func TestDistributionBuiltOncePerRepo(t *testing.T) {
	g := &fakeGit{files: fourFiles(), head: "sha1"}
	p := New(g)
	ctx := context.Background()
	p.Prewarm(ctx, "/repo")
	p.Prewarm(ctx, "/repo") // second call is a no-op
	p.Position(ctx, "/repo", "old.txt")
	if g.scans() != 1 {
		t.Fatalf("scanned %d times, want 1 (built once)", g.scans())
	}
	p.Prewarm(ctx, "/other")
	if g.scans() != 2 {
		t.Fatalf("scanned %d times, want 2 (per-repo)", g.scans())
	}
}

func TestBuildFailureIsTerminal(t *testing.T) {
	g := &fakeGit{err: context.DeadlineExceeded, head: "sha1"}
	p := New(g)
	ctx := context.Background()
	p.Prewarm(ctx, "/repo")
	if _, ok := p.Position(ctx, "/repo", "old.txt"); ok {
		t.Fatal("expected false on git failure")
	}
	p.Prewarm(ctx, "/repo") // failed build is terminal — no retry
	if g.scans() != 1 {
		t.Fatalf("scanned %d times, want 1 (no retry)", g.scans())
	}
}

// TestRefreshRescansWhenHeadMoved: a stale distribution whose repo has new
// commits is rebuilt.
func TestRefreshRescansWhenHeadMoved(t *testing.T) {
	g := &fakeGit{files: fourFiles(), head: "sha1"}
	p := New(g)
	ctx := context.Background()
	p.Prewarm(ctx, "/repo")

	grown := fourFiles()
	grown["added.txt"] = day("2024-01-01")
	g.set(grown, "sha2")
	p.refresh(ctx, "/repo") // synchronous for determinism

	if g.scans() != 2 {
		t.Fatalf("scanned %d times, want 2 (head moved → rescan)", g.scans())
	}
	pos, ok := p.Position(ctx, "/repo", "oldest.txt")
	if !ok || pos.TrackedFiles != 5 {
		t.Fatalf("distribution not refreshed: ok=%v tracked=%d", ok, pos.TrackedFiles)
	}
}

// TestRefreshSkipsScanWhenHeadUnchanged: an idle repo costs a rev-parse, not a
// rescan.
func TestRefreshSkipsScanWhenHeadUnchanged(t *testing.T) {
	g := &fakeGit{files: fourFiles(), head: "sha1"}
	p := New(g)
	ctx := context.Background()
	p.Prewarm(ctx, "/repo") // scans=1, heads=1

	p.refresh(ctx, "/repo") // HEAD still sha1

	if g.scans() != 1 {
		t.Fatalf("scanned %d times, want 1 (head unchanged → no rescan)", g.scans())
	}
}

// TestStaleReadTriggersBackgroundRefresh: crossing the TTL kicks a background
// refresh while the current index keeps serving the read.
func TestStaleReadTriggersBackgroundRefresh(t *testing.T) {
	clock := &fakeClock{t: day("2020-01-01")}
	g := &fakeGit{files: fourFiles(), head: "sha1"}
	p := New(g)
	p.now = clock.now
	ctx := context.Background()
	p.Prewarm(ctx, "/repo")

	clock.advance(10 * time.Minute) // past DefaultTTL
	grown := fourFiles()
	grown["added.txt"] = day("2024-01-01")
	g.set(grown, "sha2")

	// Reads use the cached position while its refresh is pending.
	if _, ok := p.Position(ctx, "/repo", "oldest.txt"); !ok {
		t.Fatal("stale read should still serve the current index, not block")
	}
	// The background refresh rescans and swaps in the new distribution.
	if !eventually(func() bool { return g.scans() == 2 }) {
		t.Fatal("background refresh never rescanned")
	}
	if !eventually(func() bool {
		pos, ok := p.Position(ctx, "/repo", "oldest.txt")
		return ok && pos.TrackedFiles == 5
	}) {
		t.Fatal("refreshed distribution never took effect")
	}
}

// TestInvalidateForcesRebuild: dropping the cached distribution makes the next
// read rescan (reactive invalidation on a repochange signal).
func TestInvalidateForcesRebuild(t *testing.T) {
	g := &fakeGit{files: fourFiles(), head: "sha1"}
	p := New(g)
	ctx := context.Background()
	p.Prewarm(ctx, "/repo")
	if _, ok := p.Position(ctx, "/repo", "old.txt"); !ok {
		t.Fatal("expected ready after prewarm")
	}

	p.Invalidate("/repo")

	if !eventually(func() bool { _, ok := p.Position(ctx, "/repo", "old.txt"); return ok }) {
		t.Fatal("did not rebuild after invalidate")
	}
	if g.scans() != 2 {
		t.Fatalf("scanned %d times, want 2 (rebuilt after invalidate)", g.scans())
	}
}

func TestInvalidateIsSafeForUnknownAndNil(t *testing.T) {
	New(&fakeGit{files: fourFiles(), head: "sha1"}).Invalidate("/never-built")
	var nilP *Provider
	nilP.Invalidate("/x")
}

func TestNilProviderAndNilGit(t *testing.T) {
	var nilP *Provider
	if _, ok := nilP.Position(context.Background(), "/repo", "a.txt"); ok {
		t.Fatal("nil provider should return false")
	}
	if New(nil) != nil {
		t.Fatal("New(nil git) should be nil")
	}
}

func eventually(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
