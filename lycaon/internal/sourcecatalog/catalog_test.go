package sourcecatalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogCoalescesConcurrentColdReaders(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644))
	catalog := New()
	baseBuild := catalog.build
	var builds atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		if builds.Add(1) == 1 {
			close(started)
		}
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-release:
		}
		return baseBuild(ctx, roots, walkPolicy{})
	}

	const readers = 24
	revisions := make(chan uint64, readers)
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, err := catalog.Snapshot(t.Context(), "p1", []Root{{ID: "r1", Path: root}})
			if err != nil {
				errs <- err
				return
			}
			revisions <- snapshot.Revision
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "snapshot", err)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("catalog builds = %d, want 1", got)
	}
}

func TestCatalogSnapshotCancelDoesNotAbortInFlightBuild(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644))
	catalog := New()
	baseBuild := catalog.build
	var builds atomic.Int32
	started := make(chan struct{})
	waiterCanceled := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		builds.Add(1)
		close(started)
		<-waiterCanceled
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		<-release
		return baseBuild(ctx, roots, walkPolicy{})
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := catalog.Snapshot(ctx, "p1", []Root{{ID: "r1", Path: root}})
		errCh <- err
	}()
	<-started
	cancel()
	if err := <-errCh; err == nil {
		t.Fatal("canceled snapshot returned nil error")
	}
	close(waiterCanceled)
	close(release)
	ready, err := catalog.Snapshot(context.Background(), "p1", []Root{{ID: "r1", Path: root}})
	testutil.FailErr(t, "join surviving catalog build", err)
	if ready.State != StateReady {
		t.Fatalf("catalog state = %q, want ready", ready.State)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("catalog builds = %d, want 1", got)
	}
}

func TestCatalogCurrentStartsColdGenerationWithoutWaiting(t *testing.T) {
	root := t.TempDir()
	catalog := New()
	baseBuild := catalog.build
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		close(started)
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-release:
			return baseBuild(ctx, roots, walkPolicy{})
		}
	}

	current := catalog.Current(t.Context(), "p1", []Root{{ID: "r1", Path: root}})
	if current.State != StateWarming || !current.Refreshing {
		t.Fatalf("cold current = %+v, want warming refresh", current)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("cold catalog build did not start")
	}
	close(release)
	ready, err := catalog.Snapshot(t.Context(), "p1", []Root{{ID: "r1", Path: root}})
	testutil.FailErr(t, "join catalog generation", err)
	if ready.State != StateReady {
		t.Fatalf("catalog state = %q, want ready", ready.State)
	}
}

func TestCatalogSnapshotServesAContinuouslyWrittenRoot(t *testing.T) {
	root := t.TempDir()
	catalog := New()
	var builds atomic.Int32
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		builds.Add(1)
		repochange.Notify(ctx, repochange.Event{
			ProjectDir: roots[0].Path,
			Kind:       repochange.WorktreeChanged,
			Source:     repochange.SourceMutation,
		})
		return Snapshot{
			State:   StateReady,
			Roots:   append([]Root(nil), roots...),
			Entries: []Entry{{RootID: roots[0].ID, Path: "a.txt"}},
		}, nil
	}

	snapshot, err := catalog.Snapshot(t.Context(), "p1", []Root{{ID: "r1", Path: root}})
	testutil.FailErr(t, "snapshot continuously written root", err)

	if snapshot.State != StateReady {
		t.Fatalf("catalog state = %q, want ready", snapshot.State)
	}
	if !snapshot.Moving {
		t.Fatal("snapshot.Moving = false")
	}
	if len(snapshot.Epochs) != 0 {
		t.Fatalf("snapshot.Epochs = %v, want none", snapshot.Epochs)
	}
	if len(snapshot.Entries) == 0 {
		t.Fatal("snapshot has no entries")
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("catalog builds = %d, want 1", got)
	}
}

func TestCatalogSnapshotRebuildsAfterServingAMovingGeneration(t *testing.T) {
	root := t.TempDir()
	catalog := New()
	var builds atomic.Int32
	settled := false
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		builds.Add(1)
		if !settled {
			repochange.Notify(ctx, repochange.Event{
				ProjectDir: roots[0].Path,
				Kind:       repochange.WorktreeChanged,
				Source:     repochange.SourceMutation,
			})
		}
		return Snapshot{State: StateReady, Roots: append([]Root(nil), roots...)}, nil
	}

	moving, err := catalog.Snapshot(t.Context(), "p1", []Root{{ID: "r1", Path: root}})
	testutil.FailErr(t, "first snapshot", err)
	if !moving.Moving {
		t.Fatal("first snapshot should be moving")
	}

	settled = true
	pinned, err := catalog.Snapshot(t.Context(), "p1", []Root{{ID: "r1", Path: root}})
	testutil.FailErr(t, "second snapshot", err)
	if pinned.Moving {
		t.Fatal("snapshot.Moving = true after the tree settled")
	}
	if len(pinned.Epochs) == 0 {
		t.Fatal("settled snapshot carries no epoch")
	}
}

func TestCatalogSharesPerRootGenerationsAcrossRequestShapes(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write root A", os.WriteFile(filepath.Join(rootA, "a.txt"), []byte("a"), 0o644))
	testutil.FailErr(t, "write root B", os.WriteFile(filepath.Join(rootB, "b.txt"), []byte("b"), 0o644))
	catalog := New()
	baseBuild := catalog.build
	var builds atomic.Int32
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		builds.Add(1)
		return baseBuild(ctx, roots, walkPolicy{})
	}

	_, err := catalog.Snapshot(t.Context(), "p1", []Root{{ID: "a", Path: rootA}})
	testutil.FailErr(t, "build single root", err)
	combined, err := catalog.Snapshot(t.Context(), "p1", []Root{{ID: "a", Path: rootA}, {ID: "b", Path: rootB}})
	testutil.FailErr(t, "build combined roots", err)
	if got := builds.Load(); got != 2 {
		t.Fatalf("catalog builds = %d, want one build per physical root", got)
	}
	if len(combined.Entries) != 2 {
		t.Fatalf("combined entries = %+v", combined.Entries)
	}
}

func TestCatalogBuildsSortedListingsAndBreadthFirstDirectories(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"beta/nested", "alpha/deep/leaf", "alpha/other"} {
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	for _, file := range []string{"z.txt", "alpha/a.txt"} {
		testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, file), []byte("x"), 0o644))
	}

	catalog := New()
	snapshot, err := catalog.Snapshot(t.Context(), "p1", []Root{{ID: "r1", Path: root}})
	testutil.FailErr(t, "build catalog", err)
	listing, ok := snapshot.Listing("r1", ".")
	if !ok || len(listing) != 3 {
		t.Fatalf("root listing = %+v, ok=%v", listing, ok)
	}
	if listing[0].Path != "alpha" || listing[1].Path != "beta" || listing[2].Path != "z.txt" {
		t.Fatalf("root listing order = %+v", listing)
	}
	dirs := snapshot.Directories("r1", ".")
	want := []string{".", "alpha", "beta", "alpha/deep", "alpha/other", "beta/nested", "alpha/deep/leaf"}
	if len(dirs) != len(want) {
		t.Fatalf("directories = %+v, want %v", dirs, want)
	}
	for i := range want {
		if dirs[i].Path != want[i] {
			t.Fatalf("directories = %+v, want %v", dirs, want)
		}
	}
}

func TestCatalogSingleRootSnapshotReusesPublishedGeneration(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644))
	catalog := New()
	roots := []Root{{ID: "r1", Path: root}}

	first, err := catalog.Snapshot(t.Context(), "p1", roots)
	testutil.FailErr(t, "build catalog", err)
	second, err := catalog.Snapshot(t.Context(), "p1", roots)
	testutil.FailErr(t, "read catalog", err)
	if len(first.Entries) == 0 || len(second.Entries) == 0 {
		t.Fatal("catalog generation has no entries")
	}
	if &first.Entries[0] != &second.Entries[0] {
		t.Fatal("single-root read copied the complete generation")
	}
}

func TestCatalogCoalescesAndRefreshesInvalidatedGeneration(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write first file", os.WriteFile(filepath.Join(root, "first.txt"), []byte("1"), 0o644))
	catalog := New()
	roots := []Root{{ID: "r1", Path: root}}

	first, err := catalog.Snapshot(context.Background(), "p1", roots)
	testutil.FailErr(t, "build first generation", err)
	second, err := catalog.Snapshot(context.Background(), "p1", roots)
	testutil.FailErr(t, "read cached generation", err)
	if second.Revision != first.Revision {
		t.Fatalf("cached revision = %d, want %d", second.Revision, first.Revision)
	}

	testutil.FailErr(t, "write second file", os.WriteFile(filepath.Join(root, "second.txt"), []byte("2"), 0o644))
	catalog.InvalidateRoot(root)
	refreshed, err := catalog.Snapshot(context.Background(), "p1", roots)
	testutil.FailErr(t, "refresh generation", err)
	if refreshed.Revision <= first.Revision {
		t.Fatalf("refreshed revision = %d, want > %d", refreshed.Revision, first.Revision)
	}
	listing, ok := refreshed.Listing("r1", ".")
	if !ok || len(listing) != 2 {
		t.Fatalf("refreshed listing = %+v, ok=%v", listing, ok)
	}
}

func TestCatalogKeepsLastReadyGenerationWhenRefreshFails(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644))
	catalog := New()
	roots := []Root{{ID: "r1", Path: root}}
	ready, err := catalog.Snapshot(t.Context(), "p1", roots)
	testutil.FailErr(t, "build generation", err)

	testutil.FailErr(t, "remove root", os.RemoveAll(root))
	catalog.InvalidateProject("p1")
	current := catalog.Current(t.Context(), "p1", roots)
	if current.State != StateReady || current.Revision != ready.Revision {
		t.Fatalf("stale generation = %+v, want ready revision %d", current, ready.Revision)
	}
}

// recordKeys returns the signatures the catalog currently retains.
func (c *Catalog) recordKeys() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.records))
	for key, rec := range c.records {
		out[key] = rec.scoped
	}
	return out
}

func TestScopedGenerationsDoNotEvictTheWholeRootGeneration(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mkdir subtree", os.MkdirAll(filepath.Join(root, "src"), 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "src", "a.txt"), []byte("x"), 0o644))

	catalog := New()
	catalog.rootLimit = 2
	catalog.scopedLimit = 2

	wholeRoot := []Root{{ID: "r1", Path: root}}
	_, err := catalog.Snapshot(t.Context(), "p1", wholeRoot)
	testutil.FailErr(t, "build whole-root generation", err)
	wholeRootKey := rootKey("p1", wholeRoot[0])

	// More scoped tool calls than the scoped pool holds.
	for i := range 6 {
		scoped := []Root{{ID: "r1\x00src", Path: filepath.Join(root, "src"), Within: root}}
		_, err := catalog.Snapshot(t.Context(), "p"+strings.Repeat("x", i+1), scoped)
		testutil.FailErr(t, "build scoped generation", err)
	}

	keys := catalog.recordKeys()
	if _, ok := keys[wholeRootKey]; !ok {
		t.Fatalf("whole-root generation was evicted by scoped calls; retained = %v", keys)
	}
	scopedCount := 0
	for _, scoped := range keys {
		if scoped {
			scopedCount++
		}
	}
	if scopedCount > catalog.scopedLimit {
		t.Fatalf("scoped pool holds %d generations, limit is %d", scopedCount, catalog.scopedLimit)
	}
}

func TestByteBudgetTakesFromScopedGenerationsFirst(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mkdir subtree", os.MkdirAll(filepath.Join(root, "src"), 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "src", "a.txt"), []byte("x"), 0o644))

	catalog := New()
	// One entry forces eviction on the second publish.
	catalog.byteBudget = 1

	scoped := []Root{{ID: "r1\x00src", Path: filepath.Join(root, "src"), Within: root}}
	_, err := catalog.Snapshot(t.Context(), "p1", scoped)
	testutil.FailErr(t, "build scoped generation", err)

	wholeRoot := []Root{{ID: "r1", Path: root}}
	_, err = catalog.Snapshot(t.Context(), "p1", wholeRoot)
	testutil.FailErr(t, "build whole-root generation", err)

	keys := catalog.recordKeys()
	if _, ok := keys[rootKey("p1", wholeRoot[0])]; !ok {
		t.Fatalf("byte budget evicted the whole-root generation; retained = %v", keys)
	}
	if _, ok := keys[rootKey("p1", scoped[0])]; ok {
		t.Fatalf("byte budget kept the scoped generation over the shared one; retained = %v", keys)
	}
}

func TestShedRefreshLeavesTheRecordStaleNotFailed(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644))
	catalog := New()
	roots := []Root{{ID: "r1", Path: root}}
	key := rootKey("p1", roots[0])

	// A cold record whose only build was shed carries no answer about the tree.
	catalog.claim(t.Context(), key, "p1", roots)
	catalog.publish(key, Snapshot{Roots: roots}, backgroundwork.ErrAcquireTimeout)

	catalog.mu.Lock()
	rec := catalog.records[key]
	state, stale := rec.snapshot.State, rec.stale
	catalog.mu.Unlock()
	if state == StateFailed {
		t.Fatal("a shed admission was recorded as a failed generation")
	}
	if !stale {
		t.Fatal("a shed admission left the record fresh; nothing would rebuild it")
	}

	ready, err := catalog.Snapshot(t.Context(), "p1", roots)
	testutil.FailErr(t, "rebuild after a shed refresh", err)
	if ready.State != StateReady {
		t.Fatalf("rebuild state = %q, want ready", ready.State)
	}
}
