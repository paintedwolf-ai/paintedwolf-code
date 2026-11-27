package sourcefeed

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSeedProjectWatchDoesNotResurrectRetiredRoot(t *testing.T) {
	surviving, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve surviving", err)
	detached, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve detached", err)
	detachedSub := filepath.Join(detached, "sub")
	testutil.FailErr(t, "mkdir detached sub", os.MkdirAll(detachedSub, 0o755))

	projectID := "proj-detach-seed-" + t.Name()
	t.Cleanup(func() { _ = StopProjectWatch(context.Background(), projectID) })

	var mu sync.Mutex
	var survivingPaths []string
	observer := func(_ context.Context, _ string, batch ExternalBatch) {
		mu.Lock()
		for _, ch := range batch.Changes {
			if ch.RootID == "surviving" {
				survivingPaths = append(survivingPaths, ch.Path)
			}
		}
		mu.Unlock()
	}

	EnsureProjectWatch(t.Context(), nil, projectID, "", []RootSpec{
		{ID: "surviving", WorkspaceID: "ws-1", Path: surviving},
		{ID: "detached", WorkspaceID: "ws-1", Path: detached},
	}, observer)
	SeedProjectWatch(t.Context(), projectID, []RootSeed{{
		Path: detached,
		Directories: []DirectorySpec{
			{Path: detached, EntryCount: 1},
			{Path: detachedSub, EntryCount: 0},
		},
	}})
	testutil.WaitFor(t, "both roots complete initial coverage", func() bool {
		return repochange.Coverage(surviving).Complete && repochange.Coverage(detached).Complete && repochange.DirWatched(detached, "sub")
	})

	// Rebind to surviving root only (models root detach).
	EnsureProjectWatch(t.Context(), nil, projectID, "", []RootSpec{
		{ID: "surviving", WorkspaceID: "ws-2", Path: surviving},
	}, observer)
	if repochange.Coverage(detached).Watching || repochange.DirWatched(detached, "sub") {
		t.Fatal("detached root retained watch registrations after rebind")
	}

	// Delayed SeedProjectWatch for the detached root must not resurrect it.
	SeedProjectWatch(t.Context(), projectID, []RootSeed{{
		Path: detached,
		Directories: []DirectorySpec{
			{Path: detached, EntryCount: 1},
			{Path: detachedSub, EntryCount: 0},
		},
	}})
	if repochange.Coverage(detached).Watching || repochange.DirWatched(detached, "sub") {
		t.Fatal("delayed SeedProjectWatch resurrected detached root watcher")
	}

	// Surviving root remains watched and continues delivering live writes.
	if cov := repochange.Coverage(surviving); !cov.Watching || !cov.Complete {
		t.Fatalf("surviving root lost its watch on peer detach: %+v", cov)
	}
	testutil.FailErr(t, "write surviving file", os.WriteFile(filepath.Join(surviving, "live.txt"), []byte("hi"), 0o644))
	testutil.WaitFor(t, "surviving root delivers change", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(survivingPaths, "live.txt")
	})
}

func TestCrossProjectRootRetainedWhenOneProjectDetaches(t *testing.T) {
	shared, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve shared", err)
	onlyA, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve onlyA", err)

	projA := "proj-a-" + t.Name()
	projB := "proj-b-" + t.Name()
	t.Cleanup(func() {
		_ = StopProjectWatch(context.Background(), projA)
		_ = StopProjectWatch(context.Background(), projB)
	})

	EnsureProjectWatch(t.Context(), nil, projA, "", []RootSpec{
		{ID: "shared-a", WorkspaceID: "ws-a1", Path: shared},
		{ID: "only-a", WorkspaceID: "ws-a1", Path: onlyA},
	}, nil)
	EnsureProjectWatch(t.Context(), nil, projB, "", []RootSpec{
		{ID: "shared-b", WorkspaceID: "ws-b1", Path: shared},
	}, nil)

	// Project A detaches shared; Project B still holds it.
	EnsureProjectWatch(t.Context(), nil, projA, "", []RootSpec{
		{ID: "only-a", WorkspaceID: "ws-a2", Path: onlyA},
	}, nil)
	if !repochange.Coverage(shared).Watching {
		t.Fatal("shared root lost its watch while another project still held it")
	}

	// Once Project B stops, shared root is released.
	testutil.FailErr(t, "stop projB", StopProjectWatch(t.Context(), projB))
	if repochange.Coverage(shared).Watching {
		t.Fatal("shared root retained its watch after last project released it")
	}
}
