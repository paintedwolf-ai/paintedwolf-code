package sourcecatalog

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func watchCatalogRoot(t *testing.T, dir string) {
	t.Helper()
	testutil.FailErr(t, "watch catalog root", repochange.Watch(t.Context(), dir))
	t.Cleanup(func() { repochange.Unwatch(dir) })
	testutil.WaitFor(t, "catalog root watched", func() bool {
		return repochange.DirWatched(dir, ".")
	})
}

func TestSharedCheckoutStructureCoalescesPreservesLanesAndReusesHeads(t *testing.T) {
	repochangetest.Acquire(t)
	c, first := indexFixture(t)
	c.SetScopes(testScopes{plane: sourcescope.Plane{DeferIgnored: true}})
	watchCatalogRoot(t, first.Path)

	second := Root{ID: "second-registration", Path: first.Path}
	third := Root{ID: "third-registration", Path: first.Path}
	writeIndexFile(t, first.Path, ".gitignore", "output/\n")
	for _, dir := range []string{"src", "src/deep", "output", "output/nested"} {
		writeIndexFile(t, first.Path, dir+"/file.txt", "content")
	}

	dirs := []string{".", "src", "src/deep", "output", "output/nested"}
	var statesA, statesB laneStates
	unsubA := c.Directories.SubscribeNavigation(first, func() { statesA.recordFor(t.Context(), c, "A", first, dirs) })
	defer unsubA()
	unsubB := c.Directories.SubscribeNavigation(second, func() { statesB.recordFor(t.Context(), c, "B", second, dirs) })
	defer unsubB()

	// Hold the leader's scan on "." until both Project A and Project B have started AwaitNavigation.
	rootStarted := make(chan struct{})
	releaseRoot := make(chan struct{})
	var rootOnce sync.Once
	c.Directories.onScanDirectory = func(rootPath, dir string) {
		if rootPath == first.Path && dir == "." {
			rootOnce.Do(func() {
				close(rootStarted)
				<-releaseRoot
			})
		}
	}

	errCh := make(chan error, 2)
	go func() { errCh <- c.Directories.AwaitNavigation(t.Context(), "A", first) }()
	<-rootStarted

	// Start Project B's inventory and confirm its store joined before releasing the barrier.
	testutil.FailErr(t, "warm B", c.Directories.WarmNavigation(t.Context(), "B", second))
	storeB, err := c.Trees.indexStore(t.Context(), "B", second)
	testutil.FailErr(t, "lookup storeB", err)
	testutil.WaitFor(t, "storeB joins shared flight", func() bool {
		c.Trees.sharedStructure.mu.Lock()
		defer c.Trees.sharedStructure.mu.Unlock()
		for _, f := range c.Trees.sharedStructure.inflight {
			if f.refs.Load() >= 2 {
				return true
			}
		}
		return false
	})
	go func() { errCh <- c.Directories.AwaitNavigation(t.Context(), "B", second) }()
	close(releaseRoot)

	for range 2 {
		testutil.FailErr(t, "concurrent AwaitNavigation", <-errCh)
	}
	c.Directories.onScanDirectory = nil

	afterConcurrent := c.Directories.Metrics()
	if afterConcurrent.PhysicalDirectoryReads != 5 {
		t.Fatalf("physical directory reads = %d, want 5 across 2 concurrent projects", afterConcurrent.PhysicalDirectoryReads)
	}
	if afterConcurrent.BoundaryEncodes != 2 {
		t.Fatalf("boundary encodes = %d, want 2 (1 lane + 1 head)", afterConcurrent.BoundaryEncodes)
	}
	if afterConcurrent.LanePublications != 2 || afterConcurrent.HeadPublications != 2 {
		t.Fatalf("publications: lane=%d head=%d, want 2 and 2", afterConcurrent.LanePublications, afterConcurrent.HeadPublications)
	}
	if afterConcurrent.EncodedBytes <= 0 {
		t.Fatalf("encoded bytes = %d, want > 0", afterConcurrent.EncodedBytes)
	}

	// Both projects observed the early navigable lane before collapsed trees completed, and settled complete.
	for label, st := range map[string]*laneStates{"A": &statesA, "B": &statesB} {
		st.mu.Lock()
		for _, e := range st.errs {
			testutil.FailErr(t, label+" lane state", e)
		}
		if len(st.seen) < 2 {
			t.Fatalf("project %s publications = %d, want lane + complete head", label, len(st.seen))
		}
		if !slices.Equal(st.seen[0], []bool{true, true, true, false, false}) {
			t.Fatalf("project %s first lane state = %v", label, st.seen[0])
		}
		if !slices.Equal(st.seen[len(st.seen)-1], []bool{true, true, true, true, true}) {
			t.Fatalf("project %s final state = %v", label, st.seen[len(st.seen)-1])
		}
		st.mu.Unlock()
	}

	// Both heads are independently readable with distinct RootID projections.
	for _, v := range []struct {
		project string
		root    Root
	}{{"A", first}, {"B", second}} {
		nav, openErr := c.Directories.OpenNavigation(t.Context(), v.project, v.root)
		testutil.FailErr(t, "open navigation "+v.project, openErr)
		entry, lookupErr := nav.Entry(t.Context(), "src/deep/file.txt")
		_ = nav.Close()
		testutil.FailErr(t, "lookup entry "+v.project, lookupErr)
		if entry.RootID != v.root.ID {
			t.Fatalf("project %s entry RootID = %q, want %q", v.project, entry.RootID, v.root.ID)
		}
	}

	// Sequential third registration under watched coverage reuses the ready head with zero physical reads or encodes.
	testutil.FailErr(t, "await third registration", c.Directories.AwaitNavigation(t.Context(), "C", third))
	afterThird := c.Directories.Metrics()
	if afterThird.PhysicalDirectoryReads != afterConcurrent.PhysicalDirectoryReads ||
		afterThird.BoundaryEncodes != afterConcurrent.BoundaryEncodes ||
		afterThird.EncodedBytes != afterConcurrent.EncodedBytes {
		t.Fatalf("third registration performed duplicate physical/encode work: %+v -> %+v", afterConcurrent, afterThird)
	}
	if afterThird.HeadPublications != afterConcurrent.HeadPublications+1 {
		t.Fatalf("third registration head publications = %d, want %d", afterThird.HeadPublications, afterConcurrent.HeadPublications+1)
	}

	// Repeating satisfied AwaitNavigation adds zero physical reads, encodes, or publications.
	for _, v := range []struct {
		project string
		root    Root
	}{{"A", first}, {"B", second}, {"C", third}} {
		testutil.FailErr(t, "repeat "+v.project, c.Directories.AwaitNavigation(t.Context(), v.project, v.root))
	}
	if got := c.Directories.Metrics(); got != afterThird {
		t.Fatalf("repeating satisfied AwaitNavigation added work: %+v -> %+v", afterThird, got)
	}

	// Genuine invalidation still advances all affected project views.
	_ = storeB
	writeIndexFile(t, first.Path, "src/added.txt", "fresh")
	c.InvalidateRoot(first.Path, "src/added.txt")
	for _, v := range []struct {
		project string
		root    Root
	}{{"A", first}, {"B", second}, {"C", third}} {
		testutil.WaitFor(t, "project "+v.project+" sees src/added.txt", func() bool {
			nav, openErr := c.Directories.OpenNavigation(t.Context(), v.project, v.root)
			if openErr != nil {
				return false
			}
			defer func() { _ = nav.Close() }()
			_, lookupErr := nav.Entry(t.Context(), "src/added.txt")
			return lookupErr == nil
		})
	}
}

func TestStructureDeltaDuringInitialCaptureReconcilesBeforeReadyAndPolicyIsolation(t *testing.T) {
	repochangetest.Acquire(t)
	c, root := indexFixture(t)
	c.SetScopes(testScopes{plane: sourcescope.Plane{DeferIgnored: true}})
	watchCatalogRoot(t, root.Path)

	writeIndexFile(t, root.Path, ".gitignore", "output/\n")
	writeIndexFile(t, root.Path, "src/initial.txt", "v1")
	writeIndexFile(t, root.Path, "output/heavy.txt", "v1")

	hitCollapsed := make(chan struct{})
	releaseCollapsed := make(chan struct{})
	var collapsedOnce sync.Once
	c.Directories.onScanDirectory = func(rootPath, dir string) {
		if rootPath == root.Path && dir == "output" {
			collapsedOnce.Do(func() {
				close(hitCollapsed)
				<-releaseCollapsed
			})
		}
	}

	done := make(chan error, 1)
	go func() { done <- c.Directories.AwaitNavigation(t.Context(), "A", root) }()
	<-hitCollapsed

	// Mutate an already-listed directory ("src") while initial capture is still scanning "output".
	writeIndexFile(t, root.Path, "src/during.txt", "v2")
	c.InvalidateRoot(root.Path, "src/during.txt")
	close(releaseCollapsed)
	testutil.FailErr(t, "await initial navigation", <-done)
	c.Directories.onScanDirectory = nil

	// Reconciled immediately before AwaitNavigation declared initial structure ready.
	nav, err := c.Directories.OpenNavigation(t.Context(), "A", root)
	testutil.FailErr(t, "open navigation immediately", err)
	_, lookupErr := nav.Entry(t.Context(), "src/during.txt")
	_ = nav.Close()
	testutil.FailErr(t, "during.txt must be visible immediately when AwaitNavigation returns", lookupErr)

	// Unwatched roots and incompatible OnDemand policies cannot blindly adopt peer structure without verification.
	onDemand := Root{ID: "ondemand", Path: root.Path, OnDemand: true}
	beforeOnDemand := c.Directories.Metrics()
	testutil.FailErr(t, "await on-demand root", c.Directories.AwaitNavigation(t.Context(), "D", onDemand))
	afterOnDemand := c.Directories.Metrics()
	if afterOnDemand.PhysicalDirectoryReads == beforeOnDemand.PhysicalDirectoryReads {
		t.Fatal("OnDemand root erroneously adopted eager full-tree peer structure")
	}

	unwatchedC, unwatchedRoot := indexFixture(t)
	writeIndexFile(t, unwatchedRoot.Path, "plain.txt", "v1")
	testutil.FailErr(t, "unwatched A", unwatchedC.Directories.AwaitNavigation(t.Context(), "A", unwatchedRoot))
	var unwatchedReads atomic.Int32
	unwatchedC.Directories.onScanDirectory = func(_, _ string) { unwatchedReads.Add(1) }
	testutil.FailErr(t, "unwatched B", unwatchedC.Directories.AwaitNavigation(t.Context(), "B", Root{ID: "u2", Path: unwatchedRoot.Path}))
	if unwatchedReads.Load() == 0 {
		t.Fatal("unwatched root reused peer structure without active watcher coverage")
	}
}

func TestStructuralCompactionMetricsRecordInstallAndStaleDecline(t *testing.T) {
	c := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", c.Drain(context.Background())) })
	store, err := c.Trees.indexStore(t.Context(), "A", Root{ID: "r", Path: t.TempDir()})
	testutil.FailErr(t, "indexStore", err)

	base := checkpointTestGeneration(t, store, nil, 20)
	store.mu.Lock()
	store.initializeStructureLocked()
	store.installStructureLocked(cloneStructuralGeneration(base, 1))
	store.structure.compactionDebtBytes = structuralCompactionDebtBytes
	store.structure.encodedBytes = 2 * structuralCompactionDebtBytes
	store.mu.Unlock()

	pin, err := store.retainGeneration(0, true)
	testutil.FailErr(t, "retain generation", err)
	defer pin.Release()

	// Successful compaction install.
	replacement, err := store.compactCheckpointPin(t.Context(), pin)
	testutil.FailErr(t, "compactCheckpointPin", err)
	if replacement == nil {
		t.Fatal("expected compacted replacement pin")
	}
	replacement.Release()

	m1 := c.Directories.Metrics()
	if m1.CompactionAttempts != 1 || m1.CompactionInstalls != 1 || m1.CompactionDeclines != 0 {
		t.Fatalf("after valid compaction: %+v", m1)
	}

	// Stale compaction install is safely declined without overwriting newer head.
	staleExpected := store.structure
	newer := checkpointTestGeneration(t, store, nil, 25)
	store.mu.Lock()
	store.installStructureLocked(cloneStructuralGeneration(newer, store.structure.id+1))
	wantHead := store.structure
	store.mu.Unlock()

	compacted := cloneStructuralGeneration(base, staleExpected.id+1)
	declinedPin, err := store.installCompaction(t.Context(), staleExpected, compacted)
	testutil.FailErr(t, "stale installCompaction", err)
	if declinedPin != nil {
		declinedPin.Release()
		t.Fatal("stale compaction overwrote newer head")
	}
	if store.structure != wantHead {
		t.Fatal("stale compaction mutated head pointer")
	}
	m2 := c.Directories.Metrics()
	if m2.CompactionDeclines != 1 || m2.CompactionInstalls != 1 {
		t.Fatalf("after declined stale compaction: %+v", m2)
	}
}

func (s *laneStates) recordFor(ctx context.Context, c *Catalog, project string, root Root, dirs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	navigation, err := c.Directories.OpenNavigation(ctx, project, root)
	if err != nil {
		s.errs = append(s.errs, err)
		return
	}
	defer func() { _ = navigation.Close() }()
	states := make([]bool, len(dirs))
	for i, dir := range dirs {
		state, err := navigation.State(ctx, dir)
		if err != nil {
			s.errs = append(s.errs, err)
			return
		}
		states[i] = state.Complete
	}
	s.seen = append(s.seen, states)
}
