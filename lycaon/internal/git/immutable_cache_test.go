package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestImmutableCacheCanceledWaiterDoesNotCancelSharedRead(t *testing.T) {
	var cache immutableCache[string]
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	read := func(ctx context.Context) (string, int, error) {
		calls.Add(1)
		close(started)
		select {
		case <-finish:
			return "value", 5, nil
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, _, err := cache.load(ctx, "key", read); first <- err }()
	<-started
	second := make(chan error, 1)
	go func() { _, _, err := cache.load(t.Context(), "key", read); second <- err }()
	waitCacheWaiters(t, &cache, "key", 2)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	close(finish)
	testutil.FailErr(t, "second waiter", <-second)
	value, _, err := cache.load(t.Context(), "key", read)
	testutil.FailErr(t, "shared read", err)
	if value != "value" || calls.Load() != 1 {
		t.Fatalf("value=%q reads=%d", value, calls.Load())
	}
}

func waitCacheWaiters(t *testing.T, cache *immutableCache[string], key string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		flight := cache.flights[key]
		ready := flight != nil && flight.waiters == want
		cache.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("shared readers did not join")
}

func TestImmutableCacheLastWaiterCancelsAndCannotPublish(t *testing.T) {
	var cache immutableCache[string]
	started, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, _, err := cache.load(ctx, "key", func(work context.Context) (string, int, error) {
			close(started)
			<-work.Done()
			close(canceled)
			<-finish
			return "stale", 5, nil
		})
		result <- err
	}()
	<-started
	cache.mu.Lock()
	abandoned := cache.flights["key"]
	cache.mu.Unlock()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("last waiter left a read running")
	}
	value, disposition, err := cache.load(t.Context(), "key", func(context.Context) (string, int, error) { return "fresh", 5, nil })
	testutil.FailErr(t, "new acquisition", err)
	if value != "fresh" || disposition != "miss" {
		t.Fatalf("new read: %q %q", value, disposition)
	}
	close(finish)
	<-abandoned.done
	value, disposition, err = cache.load(t.Context(), "key", func(context.Context) (string, int, error) { t.Error("missed fresh cache entry"); return "", 0, nil })
	testutil.FailErr(t, "cached new acquisition", err)
	if value != "fresh" || disposition != "hit" {
		t.Fatalf("old acquisition replaced new: %q %q", value, disposition)
	}
}

func TestImmutableCacheBoundsAndRetry(t *testing.T) {
	var cache immutableCache[string]
	for range 2 {
		_, disposition, err := cache.load(t.Context(), "oversized", func(context.Context) (string, int, error) {
			return "value", immutableEntryBytes + 1, nil
		})
		testutil.FailErr(t, "load oversized entry", err)
		if disposition != "miss" {
			t.Fatal("retained oversized entry")
		}
	}
	for i := range 10 {
		_, _, err := cache.load(t.Context(), fmt.Sprint(i), func(context.Context) (string, int, error) {
			return "value", immutableEntryBytes, nil
		})
		testutil.FailErr(t, "fill byte budget", err)
	}
	if cache.bytes > immutableCacheBytes {
		t.Fatalf("cache bytes=%d", cache.bytes)
	}
	_, disposition, err := cache.load(t.Context(), "0", func(context.Context) (string, int, error) {
		return "value", immutableEntryBytes, nil
	})
	testutil.FailErr(t, "reload evicted entry", err)
	if disposition != "miss" {
		t.Fatal("retained least recently used entry")
	}
	var calls int
	read := func(context.Context) (string, int, error) {
		calls++
		if calls == 1 {
			return "", 0, errors.New("temporary failure")
		}
		return "recovered", 9, nil
	}
	if _, _, err := cache.load(t.Context(), "retry", read); err == nil {
		t.Fatal("lost error")
	}
	value, _, err := cache.load(t.Context(), "retry", read)
	testutil.FailErr(t, "retry", err)
	if value != "recovered" || calls != 2 {
		t.Fatalf("value=%q reads=%d", value, calls)
	}
}

func TestImmutableGitCachesResolveHeadAndCopyProjections(t *testing.T) {
	dir, run := fileHistoryRepo(t)
	path := filepath.Join(dir, "a.txt")
	testutil.FailErr(t, "write original", os.WriteFile(path, []byte("one\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "original")
	first := run("rev-parse", "HEAD")
	manager := NewManager()
	trees, err := manager.TreeOIDs(t.Context(), dir, "HEAD", []string{"a.txt"})
	testutil.FailErr(t, "first tree", err)
	firstBlob := trees["a.txt"]
	trees["a.txt"] = "corrupted"
	again, err := manager.TreeOIDs(t.Context(), dir, "HEAD", []string{"a.txt"})
	testutil.FailErr(t, "cached tree", err)
	if again["a.txt"] != firstBlob {
		t.Fatal("caller mutated cached tree")
	}
	tree, err := manager.TreeOIDs(t.Context(), dir, "HEAD^{tree}", []string{"a.txt"})
	testutil.FailErr(t, "read explicit tree revision", err)
	if tree["a.txt"] != firstBlob {
		t.Fatal("tree revision resolved to different content")
	}
	testutil.FailErr(t, "write change", os.WriteFile(path, []byte("two\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "changed")
	trees, err = manager.TreeOIDs(t.Context(), dir, "HEAD", []string{"a.txt"})
	testutil.FailErr(t, "moved tree", err)
	if trees["a.txt"] == firstBlob {
		t.Fatal("symbolic HEAD reused stale tree")
	}
	options := CommitReviewOptions{Before: first, After: run("rev-parse", "HEAD"), Limit: 1}
	metadata, err := manager.CommitDetails(t.Context(), dir, options.After)
	testutil.FailErr(t, "read commit metadata", err)
	metadata.Parents[0] = "corrupted"
	metadata, err = manager.CommitDetails(t.Context(), dir, options.After)
	testutil.FailErr(t, "read cached metadata", err)
	if metadata.Parents[0] != first || manager.commits.order.Len() != 1 {
		t.Fatal("commit metadata cache was mutated or not shared")
	}
	review, err := manager.CommitReview(t.Context(), dir, options)
	testutil.FailErr(t, "review", err)
	review.Files[0].Path = "corrupted"
	review, err = manager.CommitReview(t.Context(), dir, options)
	testutil.FailErr(t, "cached review", err)
	if review.Files[0].Path != "a.txt" {
		t.Fatal("caller mutated cached review")
	}
	options.Offset = 1
	page, err := manager.CommitReview(t.Context(), dir, options)
	testutil.FailErr(t, "last page", err)
	if len(page.Files) != 0 || page.Total != 1 || manager.reviews.order.Len() != 1 {
		t.Fatalf("page=%+v cached=%d", page, manager.reviews.order.Len())
	}
}

func TestCommitReviewCacheStopsAtBudgetAndStreamsOversizedPages(t *testing.T) {
	parser := newCommitReviewParser(CommitReviewOptions{Limit: 100000})
	parser.bounded = true
	metadata := []byte(":100644 100644 " + strings.Repeat("a", 40) + " " + strings.Repeat("b", 40) + " M")
	var err error
	for index := 0; index < 20000; index++ {
		testutil.FailErr(t, "parse metadata", parser.raw(metadata))
		err = parser.raw([]byte(fmt.Sprintf("file-%d.txt", index)))
		if err != nil {
			break
		}
	}
	if !errors.Is(err, errReviewCacheLimit) {
		t.Fatalf("unbounded comparison: rows=%d err=%v", len(parser.page.Files), err)
	}
	// Streaming retains only the selected page.
	parser = newCommitReviewParser(CommitReviewOptions{Offset: 3, Limit: 2})
	for index := 0; index < 20000; index++ {
		testutil.FailErr(t, "stream metadata", parser.raw(metadata))
		testutil.FailErr(t, "stream path", parser.raw([]byte(fmt.Sprintf("file-%d.txt", index))))
	}
	if len(parser.page.Files) != 2 || parser.page.Total != 20000 || parser.page.Files[0].Path != "file-3.txt" {
		t.Fatalf("streamed page: %+v", parser.page)
	}
}

func TestImmutableCacheEntryLimitAndRecency(t *testing.T) {
	var cache immutableCache[string]
	read := func(context.Context) (string, int, error) { return "value", 1, nil }
	for i := range 128 {
		_, _, err := cache.load(t.Context(), fmt.Sprint(i), read)
		testutil.FailErr(t, "fill entry budget", err)
	}
	_, disposition, err := cache.load(t.Context(), "0", read)
	testutil.FailErr(t, "refresh oldest entry", err)
	if disposition != "hit" {
		t.Fatal("missing oldest entry")
	}
	_, _, err = cache.load(t.Context(), "new", read)
	testutil.FailErr(t, "exceed entry budget", err)
	if len(cache.entries) != 128 {
		t.Fatalf("unbounded entry count: %d", len(cache.entries))
	}
	_, disposition, err = cache.load(t.Context(), "1", read)
	testutil.FailErr(t, "reload oldest entry", err)
	if disposition != "miss" {
		t.Fatal("least recent entry survived eviction")
	}
	_, disposition, err = cache.load(t.Context(), "0", read)
	testutil.FailErr(t, "read recent entry", err)
	if disposition != "hit" {
		t.Fatal("recently used entry evicted")
	}
}

func TestImmutableCachePanicSettlesAndAllowsRetry(t *testing.T) {
	var cache immutableCache[string]
	_, _, err := cache.load(t.Context(), "key", func(context.Context) (string, int, error) { panic("broken read") })
	if err == nil {
		t.Fatal("panic did not fail acquisition")
	}
	value, _, err := cache.load(t.Context(), "key", func(context.Context) (string, int, error) { return "retry", 5, nil })
	testutil.FailErr(t, "retry recovered acquisition", err)
	if value != "retry" {
		t.Fatalf("retry=%q", value)
	}
}
