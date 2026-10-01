package sourcefeed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectWatchOverflowPublishesOneInvalidation(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	root := RootSpec{ID: "r1", WorkspaceID: "ws", Path: t.TempDir()}
	var mu sync.Mutex
	var observed []ExternalBatch
	w := &projectWatch{projectID: "project-overflow", roots: []RootSpec{root}, external: func(_ context.Context, _ string, batch ExternalBatch) {
		mu.Lock()
		observed = append(observed, batch)
		mu.Unlock()
	}}
	w.changes = newChangeConverger(10*time.Millisecond, 50*time.Millisecond, w.flushExternalChanges)
	t.Cleanup(func() { w.changes.close(t.Context()) })
	for i := 0; i <= maxChangesPerEvent; i++ {
		w.changes.queue(t.Context(), root, fmt.Sprintf("churn/file-%d.txt", i))
	}
	testutil.WaitFor(t, time.Second, func() bool { return len(cap.batches()) == 1 })
	batch := cap.batches()[0]
	if !batch.Resync || len(batch.Changes) != 0 {
		t.Fatalf("overflow published a path list: resync=%t changes=%d", batch.Resync, len(batch.Changes))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 1 || !observed[0].Resync || len(observed[0].Changes) != 0 {
		t.Fatalf("observer batch = %+v", observed)
	}
}

func TestEnsureProjectWatchKeepsRoutingForSameRoots(t *testing.T) {
	root := t.TempDir()
	const projectID = "project-rebind"
	roots := []RootSpec{{ID: "r1", WorkspaceID: "ws", Path: root}}
	if !EnsureProjectWatch(t.Context(), projectID, "", roots, nil) {
		t.Fatal("initial watch did not report a new binding")
	}
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })
	watchRegMu.Lock()
	first := watchers[watchKey{projectID, ""}].changes
	watchRegMu.Unlock()
	if EnsureProjectWatch(t.Context(), projectID, "", roots, nil) {
		t.Fatal("unchanged roots reported a new binding")
	}
	watchRegMu.Lock()
	second := watchers[watchKey{projectID, ""}].changes
	watchRegMu.Unlock()
	if first != second {
		t.Fatal("a rebind with the same roots replaced the pending change window")
	}
	if !EnsureProjectWatch(t.Context(), projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws-2", Path: root}}, nil) {
		t.Fatal("changed roots did not report a new binding")
	}
	watchRegMu.Lock()
	third := watchers[watchKey{projectID, ""}].changes
	watchRegMu.Unlock()
	if third == second {
		t.Fatal("a rebind with different roots kept the stale routing")
	}
}

func TestWatchNeedsSeedFollowsPlatformCoverage(t *testing.T) {
	root := t.TempDir()
	if WatchNeedsSeed(root) {
		t.Fatal("an unbound root asked for a seed")
	}
	const projectID = "project-seed"
	EnsureProjectWatch(t.Context(), projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws", Path: root}}, nil)
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })
	coverage := repochange.Coverage(root)
	if !coverage.Watching {
		t.Fatal("binding did not start the root watcher")
	}
	want := !watchfd.Recursive || coverage.Truncated > 0
	if got := WatchNeedsSeed(root); got != want {
		t.Fatalf("WatchNeedsSeed = %v, want %v for coverage %+v", got, want, coverage)
	}
	SeedProjectWatch(t.Context(), projectID, []RootSeed{{Path: root, Directories: []DirectorySpec{{Path: root, EntryCount: 0}}}})
	if got := repochange.Coverage(root); !got.Watching {
		t.Fatalf("seeding dropped the watch: %+v", got)
	}
}

func TestProjectWatchRoutesSharedWatcherEvent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "changed.go")
	testutil.FailErr(t, "write changed file", os.WriteFile(path, []byte("package changed"), 0o644))
	seen := make(chan string, 1)
	const projectID = "project-shared-watch"
	EnsureProjectWatch(t.Context(), projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws", Path: root}}, func(_ context.Context, project string, _ ExternalBatch) {
		seen <- project
	})
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })
	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged,
		Paths: []string{"changed.go"}, Source: repochange.SourceWatcher,
	})
	if got := <-seen; got != projectID {
		t.Fatalf("routed event = %q", got)
	}
}

func TestProjectWatchIgnoresHostMutationEvent(t *testing.T) {
	root := t.TempDir()
	seen := false
	const projectID = "project-mutation-watch"
	EnsureProjectWatch(t.Context(), projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws", Path: root}}, func(context.Context, string, ExternalBatch) {
		seen = true
	})
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })
	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged,
		Paths: []string{"changed.go"}, Source: repochange.SourceMutation,
	})
	if seen {
		t.Fatal("host mutation was classified as an external source edit")
	}
}

func TestProjectWatchFiltersHostWriteAtFlush(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	root := RootSpec{ID: "r1", WorkspaceID: "ws", Path: t.TempDir()}
	target := filepath.Join(root.Path, "changed.go")
	testutil.FailErr(t, "write changed file", os.WriteFile(target, []byte("package changed"), 0o644))
	var reconciles atomic.Int32
	w := &projectWatch{projectID: "project-host-write", external: func(context.Context, string, ExternalBatch) {
		reconciles.Add(1)
	}}
	NoteHostWrite(target)
	w.flushExternalChanges(t.Context(), pendingExternalBatch{
		changes: []pendingExternalChange{{root: root, rel: "changed.go"}},
	})
	if len(cap.events()) != 0 || reconciles.Load() != 0 {
		t.Fatalf("host echo emitted=%d reconciled=%d", len(cap.events()), reconciles.Load())
	}
}

func TestProjectWatchResyncDoesNotMisattributeHostWrite(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	root := RootSpec{ID: "r1", WorkspaceID: "ws", Path: t.TempDir()}
	target := filepath.Join(root.Path, "changed.go")
	testutil.FailErr(t, "write changed file", os.WriteFile(target, []byte("package changed"), 0o644))
	var reconciles atomic.Int32
	w := &projectWatch{projectID: "project-host-resync", roots: []RootSpec{root}, external: func(context.Context, string, ExternalBatch) {
		reconciles.Add(1)
	}}
	NoteHostWrite(target)
	w.flushExternalChanges(t.Context(), pendingExternalBatch{
		changes: []pendingExternalChange{{root: root, rel: "changed.go"}},
		resync:  true,
	})
	batches := cap.batches()
	if len(batches) != 1 || !batches[0].Resync || len(batches[0].Changes) != 0 {
		t.Fatalf("host write resync = %+v", batches)
	}
	if reconciles.Load() != 1 {
		t.Fatalf("resync reconciles=%d want 1", reconciles.Load())
	}
}

func TestProjectWatchPublishesWatcherResyncWithoutPaths(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	root := t.TempDir()
	var reconciles atomic.Int32
	w := &projectWatch{
		projectID: "project-resync",
		roots:     []RootSpec{{ID: "r1", WorkspaceID: "ws", Path: root}},
		external:  func(context.Context, string, ExternalBatch) { reconciles.Add(1) },
	}
	w.changes = newChangeConverger(10*time.Millisecond, 50*time.Millisecond, w.flushExternalChanges)
	t.Cleanup(func() { w.changes.close(t.Context()) })
	w.observe(t.Context(), repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged,
		Paths: []string{"."}, Source: repochange.SourceWatcher,
	})
	testutil.WaitFor(t, time.Second, func() bool { return len(cap.batches()) == 1 })
	batches := cap.batches()
	if !batches[0].Resync || len(batches[0].Changes) != 0 {
		t.Fatalf("watcher resync event = %+v", batches[0])
	}
	if reconciles.Load() != 1 {
		t.Fatalf("resync reconciles=%d want 1", reconciles.Load())
	}
}

func TestProjectWatchIndexChangeRefreshesWithoutFileOrHeadEvents(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	root := t.TempDir()
	w := &projectWatch{projectID: "index-refresh", roots: []RootSpec{{ID: "r1", WorkspaceID: "ws", Path: root}}}
	w.changes = newChangeConverger(10*time.Millisecond, 50*time.Millisecond, w.flushExternalChanges)
	t.Cleanup(func() { w.changes.close(t.Context()) })
	w.observe(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.IndexChanged, Source: repochange.SourceWatcher})
	testutil.WaitFor(t, time.Second, func() bool { return len(cap.batches()) == 1 })
	batch := cap.batches()[0]
	if !batch.Resync || len(batch.Changes) != 0 || batch.GitChanged {
		t.Fatalf("index refresh fabricated changes: %+v", batch)
	}
}

func TestProjectWatchFiltersGitMetadataSegments(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	root := t.TempDir()
	w := &projectWatch{projectID: "metadata-filter", roots: []RootSpec{{ID: "r1", WorkspaceID: "ws", Path: root}}}
	w.changes = newChangeConverger(time.Hour, time.Hour, w.flushExternalChanges)
	t.Cleanup(func() { w.changes.close(t.Context()) })
	w.observe(t.Context(), repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Source: repochange.SourceWatcher,
		Paths: []string{".git", ".git/HEAD", "nested/.git/config", ".gitignore", ".github/workflows/build.yml", "nested/.gitkeep"},
	})
	w.changes.close(t.Context())
	var paths []string
	for _, event := range cap.events() {
		paths = append(paths, event.Path)
	}
	want := []string{".github/workflows/build.yml", ".gitignore", "nested/.gitkeep"}
	if !slices.Equal(paths, want) {
		t.Fatalf("project file events = %v, want %v", paths, want)
	}
}

func TestProjectWatchSuppressesPublishedWriteEcho(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	root := RootSpec{ID: "r1", WorkspaceID: "ws", Path: t.TempDir()}
	w := &projectWatch{projectID: "write-echo", roots: []RootSpec{root}}
	w.changes = newChangeConverger(time.Hour, time.Hour, w.flushExternalChanges)
	t.Cleanup(func() { w.changes.close(t.Context()) })
	for _, name := range []string{"host.txt", "external.txt"} {
		testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root.Path, name), []byte("content"), 0o644))
	}
	Emit(t.Context(), Change{
		ProjectID: w.projectID, WorkspaceID: root.WorkspaceID, RootID: root.ID,
		WorkspaceKind: api.SourceWorkspaceKindProject, Path: "host.txt", AbsPath: filepath.Join(root.Path, "host.txt"),
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
	})
	w.observe(t.Context(), repochange.Event{
		ProjectDir: root.Path, Kind: repochange.WorktreeChanged, Source: repochange.SourceWatcher,
		Paths: []string{"host.txt", "external.txt"},
	})
	w.changes.close(t.Context())
	events := cap.events()
	if len(events) != 2 || events[0].Path != "host.txt" || events[0].Origin != api.SourceChangeOriginUser ||
		events[1].Path != "external.txt" || events[1].Origin != api.SourceChangeOriginExternal {
		t.Fatalf("published events = %+v", events)
	}
}

func TestProjectWatchKeepsBothCheckoutScopes(t *testing.T) {
	const projectID = "multiple-checkouts"
	base, checkout := t.TempDir(), t.TempDir()
	seen := make(chan string, 4)
	for scope, root := range map[string]string{"": base, "checkout": checkout} {
		testutil.FailErr(t, "write watched file", os.WriteFile(filepath.Join(root, "a.txt"), []byte(scope), 0o600))
		EnsureProjectWatch(t.Context(), projectID, scope, []RootSpec{{ID: "r1", WorkspaceID: "ws-" + scope, Path: root}}, func(_ context.Context, _ string, batch ExternalBatch) {
			if batch.Resync || batch.HeadMoved {
				seen <- "ws-" + scope
			}
			for _, change := range batch.Changes {
				seen <- change.WorkspaceID
			}
		})
	}
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })
	for _, root := range []string{base, checkout} {
		repochange.Notify(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Source: repochange.SourceWatcher, Paths: []string{"a.txt"}})
	}
	observed := map[string]bool{}
	for len(observed) < 2 {
		select {
		case id := <-seen:
			observed[id] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("lost checkout coverage: %v", observed)
		}
	}
	if !observed["ws-"] || !observed["ws-checkout"] {
		t.Fatalf("misrouted checkouts: %v", observed)
	}
	StopProjectWatch(t.Context(), projectID)
	for _, root := range []string{base, checkout} {
		if repochange.Coverage(root).Watching {
			t.Fatal("project close left a checkout watched")
		}
	}
}
