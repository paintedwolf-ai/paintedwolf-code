package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestObserveServesMovingGenerationWhileReconciling(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	catalog := New()
	var builds atomic.Int32
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		switch builds.Add(1) {
		case 1:
			repochange.Notify(ctx, repochange.Event{
				ProjectDir: roots[0].Path,
				Kind:       repochange.WorktreeChanged,
				Source:     repochange.SourceMutation,
			})
		case 2:
			close(refreshStarted)
			select {
			case <-releaseRefresh:
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			}
		}
		return Snapshot{State: StateReady, Roots: append([]Root(nil), roots...)}, nil
	}

	roots := []Root{{ID: "root", Path: rootPath}}
	snapshot, err := catalog.Snapshot(t.Context(), "project", roots)
	if err != nil {
		testutil.FailErr(t, "build moving generation", err)
	}
	if !snapshot.Moving || len(snapshot.Epochs) != 0 || builds.Load() != 1 {
		t.Fatalf("snapshot = {moving:%t epochs:%d builds:%d}", snapshot.Moving, len(snapshot.Epochs), builds.Load())
	}

	current, err := catalog.Observe(t.Context(), "project", roots)
	if err != nil {
		testutil.FailErr(t, "observe moving generation", err)
	}
	if !current.Moving || !current.Refreshing {
		t.Fatalf("current = {moving:%t refreshing:%t}", current.Moving, current.Refreshing)
	}
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("background reconciliation did not start")
	}
	close(releaseRefresh)
	pinned, err := catalog.Snapshot(t.Context(), "project", roots)
	if err != nil {
		testutil.FailErr(t, "join reconciliation", err)
	}
	if pinned.Moving || len(pinned.Epochs) == 0 || builds.Load() != 2 {
		t.Fatalf("pinned = {moving:%t epochs:%d builds:%d}", pinned.Moving, len(pinned.Epochs), builds.Load())
	}
}

func TestObserveJoinsKnownInvalidationOnce(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	catalog := New()
	var builds atomic.Int32
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		if builds.Add(1) == 2 {
			repochange.Notify(ctx, repochange.Event{
				ProjectDir: roots[0].Path,
				Kind:       repochange.WorktreeChanged,
				Source:     repochange.SourceMutation,
			})
			close(buildStarted)
			select {
			case <-releaseBuild:
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			}
		}
		return Snapshot{State: StateReady, Roots: append([]Root(nil), roots...)}, nil
	}

	roots := []Root{{ID: "root", Path: rootPath}}
	_, err := catalog.Snapshot(t.Context(), "project", roots)
	testutil.FailErr(t, "build initial generation", err)
	catalog.InvalidateRoot(rootPath)

	type obsResult struct {
		snap Snapshot
		err  error
	}
	obsCh := make(chan obsResult, 1)
	go func() {
		snap, err := catalog.Observe(t.Context(), "project", roots)
		obsCh <- obsResult{snap, err}
	}()

	select {
	case <-buildStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("build 2 did not start")
	}
	close(releaseBuild)

	res := <-obsCh
	testutil.FailErr(t, "observe invalidated generation", res.err)
	if !res.snap.Moving || len(res.snap.Epochs) != 0 || builds.Load() != 2 {
		t.Fatalf("snapshot = {moving:%t epochs:%d builds:%d}", res.snap.Moving, len(res.snap.Epochs), builds.Load())
	}
}

func TestSettlementBoundsReuseWithoutCompleteWatcherCoverage(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "first.txt"), []byte("one"), 0o600); err != nil {
		testutil.FailErr(t, "write first source", err)
	}

	now := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	catalog := New()
	catalog.now = func() time.Time { return now }
	root := Root{ID: "root", Path: rootPath}
	first, err := catalog.Snapshot(context.Background(), "project", []Root{root})
	if err != nil {
		testutil.FailErr(t, "build first snapshot", err)
	}
	if len(first.Entries) != 1 {
		t.Fatalf("first entries = %d, want 1", len(first.Entries))
	}
	if _, settled := catalog.CurrentSettled(context.Background(), "project", []Root{root}); !settled {
		t.Fatal("new snapshot should be reusable inside incomplete-coverage TTL")
	}

	if err := os.WriteFile(filepath.Join(rootPath, "second.txt"), []byte("two"), 0o600); err != nil {
		testutil.FailErr(t, "write late source", err)
	}
	now = now.Add(incompleteCoverageTTL + time.Millisecond)
	if _, settled := catalog.CurrentSettled(context.Background(), "project", []Root{root}); settled {
		t.Fatal("snapshot past incomplete-coverage TTL reported fresh")
	}
	second, err := catalog.Snapshot(context.Background(), "project", []Root{root})
	if err != nil {
		testutil.FailErr(t, "reconcile late source", err)
	}
	if len(second.Entries) != 2 {
		t.Fatalf("reconciled entries = %d, want 2", len(second.Entries))
	}
}

func TestSettlementKeepsRevisionWhenTreeIsUnchanged(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "source.txt"), []byte("one"), 0o600); err != nil {
		testutil.FailErr(t, "write source", err)
	}

	now := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	catalog := New()
	catalog.now = func() time.Time { return now }
	root := Root{ID: "root", Path: rootPath}
	first, err := catalog.Snapshot(context.Background(), "project", []Root{root})
	testutil.FailErr(t, "build first snapshot", err)

	now = now.Add(incompleteCoverageTTL + time.Millisecond)
	second, err := catalog.Observe(context.Background(), "project", []Root{root})
	testutil.FailErr(t, "revalidate source", err)
	if second.Revision != first.Revision {
		t.Fatalf("revalidated revision = %d, want %d", second.Revision, first.Revision)
	}
}

func TestObserveWithinReturnsAnExpiredWaitWithoutAGeneration(t *testing.T) {
	t.Parallel()
	catalog := New()
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	var builds atomic.Int32
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		builds.Add(1)
		close(buildStarted)
		select {
		case <-releaseBuild:
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{State: StateReady, Roots: append([]Root(nil), roots...)}, nil
	}
	roots := []Root{{ID: "root", Path: t.TempDir()}}

	_, _, err := catalog.ObserveWithin(t.Context(), "project", roots, 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ObserveWithin error = %v, want an expired wait", err)
	}
	select {
	case <-buildStarted:
	case <-time.After(time.Second):
		t.Fatal("the build did not start")
	}

	close(releaseBuild)
	snapshot, fresh, err := catalog.ObserveWithin(t.Context(), "project", roots, 5*time.Second)
	if err != nil {
		testutil.FailErr(t, "observe after the build", err)
	}
	if snapshot.State != StateReady || !fresh || builds.Load() != 1 {
		t.Fatalf("observe = {state:%s fresh:%t builds:%d}; the expired wait must not cancel or repeat the build", snapshot.State, fresh, builds.Load())
	}
}

func TestObserveWithinServesTheCompleteGenerationWhileItIsStale(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	catalog := New()
	var builds atomic.Int32
	rebuildStarted := make(chan struct{})
	releaseRebuild := make(chan struct{})
	catalog.build = func(ctx context.Context, roots []Root, _ walkPolicy) (Snapshot, error) {
		if builds.Add(1) > 1 {
			select {
			case <-rebuildStarted:
			default:
				close(rebuildStarted)
			}
			select {
			case <-releaseRebuild:
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			}
		}
		return Snapshot{State: StateReady, Roots: append([]Root(nil), roots...)}, nil
	}
	roots := []Root{{ID: "root", Path: rootPath}}
	first, err := catalog.Snapshot(t.Context(), "project", roots)
	if err != nil {
		testutil.FailErr(t, "build the first generation", err)
	}
	if first.State != StateReady || first.Moving {
		t.Fatalf("first generation = {state:%s moving:%t}", first.State, first.Moving)
	}

	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: rootPath,
		Kind:       repochange.WorktreeChanged,
		Source:     repochange.SourceMutation,
	})
	served, fresh, err := catalog.ObserveWithin(t.Context(), "project", roots, 50*time.Millisecond)
	if err != nil {
		testutil.FailErr(t, "observe while stale", err)
	}
	if served.State != StateReady || fresh {
		t.Fatalf("stale observe = {state:%s fresh:%t}, want the complete generation reported stale", served.State, fresh)
	}
	select {
	case <-rebuildStarted:
	case <-time.After(time.Second):
		t.Fatal("the replacing build did not start")
	}

	close(releaseRebuild)
	current, fresh, err := catalog.ObserveWithin(t.Context(), "project", roots, 5*time.Second)
	if err != nil {
		testutil.FailErr(t, "observe after the rebuild", err)
	}
	if current.State != StateReady || !fresh {
		t.Fatalf("after rebuild = {state:%s fresh:%t}", current.State, fresh)
	}
}
