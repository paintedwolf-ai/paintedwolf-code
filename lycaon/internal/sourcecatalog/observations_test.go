package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryObservationsArePagedAndKeepConsumerVisibility(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := 0; i < 600; i++ {
		writeIndexFile(t, root.Path, fmt.Sprintf("file-%04d.txt", i), "content")
	}
	writeIndexFile(t, root.Path, ".paintedwolf/settings.yaml", "project settings")
	writeIndexFile(t, root.Path, ".git/config", "metadata")
	observed, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe immediate directory", err)
	if !observed.Complete {
		t.Fatal("directory was not completed")
	}
	repeated, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "reuse immediate observation", err)
	if repeated.Sequence != observed.Sequence {
		t.Fatal("warm directory was enumerated again")
	}
	count, after := 0, ""
	names := []string{}
	for {
		entries, status, err := navigationEntries(t, catalog, root, ".", after, 100)
		testutil.FailErr(t, "directory page", err)
		if len(entries) > 100 || !status.Complete {
			t.Fatal("page violated its bound or coverage")
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			names = append(names, entry.Name)
		}
		count += len(entries)
		last := entries[len(entries)-1]
		after = DirectoryOrder(last.Name, last.IsDir)
	}
	if count != 602 || !slices.Contains(names, ".paintedwolf") || !slices.Contains(names, ".git") {
		t.Fatalf("membership count %d", count)
	}
	_, err = catalog.ObserveDirectory(t.Context(), "p", root, ".paintedwolf", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe overlay", err)
	reader := waitIndex(t, catalog, root)
	paths, err := reader.FilePathsPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", TreeFilePageLimit)
	testutil.FailErr(t, "catalog projection", err)
	if !slices.Contains(paths, ".paintedwolf/settings.yaml") {
		t.Fatal("human search omitted the overlay")
	}
	if slices.Contains(paths, ".git/config") {
		t.Fatal("search enrichment admitted VCS metadata")
	}
	agentPaths, err := reader.FilePathsPage(t.Context(), FileScope{Audience: AgentAudience, IncludeHidden: true}, "", TreeFilePageLimit)
	testutil.FailErr(t, "agent projection after human discovery", err)
	if slices.Contains(agentPaths, ".paintedwolf/settings.yaml") {
		t.Fatal("human discovery widened agent metadata scope")
	}
	entries, _, err := navigationEntries(t, catalog, root, ".paintedwolf", "", 100)
	testutil.FailErr(t, "overlay after catalog refresh", err)
	if len(entries) != 1 || entries[0].Name != "settings.yaml" {
		t.Fatal("catalog removed human-visible metadata")
	}
}

func TestDirectoryObservationsRejectEscapesAndSymlinkExpansion(t *testing.T) {
	catalog, root := indexFixture(t)
	outside := t.TempDir()
	testutil.FailErr(t, "create link", os.Symlink(outside, filepath.Join(root.Path, "link")))
	for _, dir := range []string{"../", outside, "link"} {
		_, err := catalog.ObserveDirectory(context.Background(), "p", root, dir, DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		if err == nil {
			t.Fatalf("accepted forbidden directory %q", dir)
		}
	}
}

func TestDirectoryRefreshRemovesVanishedSubtrees(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "gone/child.txt", "old")
	for _, dir := range []string{".", "gone"} {
		_, err := catalog.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "initial observation", err)
	}
	testutil.FailErr(t, "remove fixture subtree", os.RemoveAll(filepath.Join(root.Path, "gone")))
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get observation store", err)
	store.mu.Lock()
	store.invalidateObservationsLocked(nil)
	store.mu.Unlock()
	_, err = catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "refresh root", err)
	entries, _, err := navigationEntries(t, catalog, root, ".", "", 100)
	testutil.FailErr(t, "read refreshed membership", err)
	if len(entries) != 0 {
		t.Fatalf("retained vanished entries: %+v", entries)
	}
	navigation, err := catalog.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open refreshed structure", err)
	defer func() { _ = navigation.Close() }()
	children, err := navigation.Children(t.Context(), "gone")
	testutil.FailErr(t, "open vanished descendants", err)
	count, err := children.Count(t.Context())
	testutil.FailErr(t, "count vanished descendants", err)
	if count != 0 {
		t.Fatal("vanished descendant remained in the current generation")
	}
}

func TestDirectoryRefreshCannotResurrectDeletedOpenDirectory(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "gone/child.txt", "old")
	var stale DirectoryObservation
	for _, dir := range []string{".", "gone"} {
		observation, err := catalog.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "initial discovery", err)
		if dir == "gone" {
			stale = observation
		}
	}
	testutil.FailErr(t, "delete discovered directory", os.RemoveAll(filepath.Join(root.Path, "gone")))
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get shared store", err)
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "pin stale generation", err)
	defer pin.Release()
	store.mu.Lock()
	store.invalidateObservationsLocked(nil)
	store.mu.Unlock()
	// Parent deletion fences listings opened after the watcher event.
	stale.Invalidation = store.observationMark("gone")
	_, err = catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "publish deletion", err)
	builder, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create stale builder", err)
	defer builder.close()
	testutil.FailErr(t, "prepare stale directory", builder.observe(t.Context(), directoryDiscovery{observation: stale}))
	testutil.FailErr(t, "finalize stale directory", builder.finalize(t.Context()))
	err = store.publishStructure(t.Context(), builder, pin.Generation)
	testutil.FailErr(t, "merge stale scan without restoring deleted membership", err)
	if _, err := store.readObservation(t.Context(), "gone"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("late scan restored deleted directory observation: %v", err)
	}
	current, err := catalog.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open current generation", err)
	defer func() { _ = current.Close() }()
	if _, err := current.Entry(t.Context(), "gone"); err == nil {
		t.Fatal("late directory publication resurrected removed entry")
	}
}

func TestDirectorySymlinksRemainManuallyNavigableWithoutRecursiveWeight(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "actual/child.txt", "data")
	testutil.FailErr(t, "create internal directory link", os.Symlink("actual", filepath.Join(root.Path, "alias")))
	for _, dir := range []string{".", "alias"} {
		_, err := catalog.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "observe linked directory", err)
	}
	entries, _, err := navigationEntries(t, catalog, root, "alias", "", 10)
	testutil.FailErr(t, "read linked membership", err)
	if len(entries) != 1 || entries[0].Path != "alias/child.txt" {
		t.Fatalf("linked membership: %+v", entries)
	}
	navigation, err := catalog.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open navigation snapshot", err)
	defer func() { _ = navigation.Close() }()
	entry, err := navigation.Entry(t.Context(), "alias")
	testutil.FailErr(t, "read directory link", err)
	if !entry.IsDir || !entry.IsSymlink {
		t.Fatalf("link lost its kind: %+v", entry)
	}
	_, weight, err := navigation.ChildRank(t.Context(), ".", entry, true)
	testutil.FailErr(t, "read recursive link weight", err)
	if weight != 1 {
		t.Fatalf("recursive expansion followed a link: weight=%d", weight)
	}
}

func TestIndexDiscoveryStopsAtRemainingConsumerBudget(t *testing.T) {
	catalog, root := indexFixture(t)
	catalog.SetScopes(testScopes{plane: sourcescope.Plane{Budgets: sandbox.SurveyBudgets{WalkEntries: 10}}})
	for i := 0; i < 100; i++ {
		writeIndexFile(t, root.Path, fmt.Sprintf("f-%03d.txt", i), "data")
	}
	store, walk := indexWalkFixture(t, catalog, root)
	stepIndexDir(t, walk)
	observed, err := store.readObservation(t.Context(), ".")
	testutil.FailErr(t, "read limited discovery", err)
	if observed.Complete || observed.Entries != 11 {
		t.Fatalf("walk budget over-discovered: %+v", observed)
	}
	observed, err = catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "continue human discovery independently", err)
	if !observed.Complete || observed.Entries != 100 {
		t.Fatalf("human discovery inherited index cap: %+v", observed)
	}
}

func TestDirectoryAbsoluteInternalLinksPreserveNavigation(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "actual/nested/child.txt", "data")
	testutil.FailErr(t, "create absolute internal link", os.Symlink(filepath.Join(root.Path, "actual"), filepath.Join(root.Path, "alias")))
	testutil.FailErr(t, "create dangling link", os.Symlink("missing", filepath.Join(root.Path, "dangling")))
	writeIndexFile(t, root.Path, ".git/config", "repository metadata")
	testutil.FailErr(t, "create metadata alias", os.Symlink(".git", filepath.Join(root.Path, "metadata")))
	for _, dir := range []string{".", "alias", "alias/nested"} {
		_, err := catalog.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "observe internal link", err)
	}
	entries, _, err := navigationEntries(t, catalog, root, "alias/nested", "", 10)
	testutil.FailErr(t, "read linked descendant", err)
	if len(entries) != 1 || entries[0].Path != "alias/nested/child.txt" {
		t.Fatalf("linked descendants: %+v", entries)
	}
	entries, _, err = navigationEntries(t, catalog, root, ".", "", 10)
	testutil.FailErr(t, "read link classification", err)
	for _, entry := range entries {
		if entry.Name == "alias" && (!entry.IsDir || !entry.IsSymlink) {
			t.Fatalf("absolute link classification: %+v", entry)
		}
		if entry.Name == "dangling" {
			t.Fatal("dangling link was not omitted")
		}
	}
	_, err = catalog.ObserveDirectory(t.Context(), "p", root, "metadata", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe metadata alias", err)
	entries, _, err = navigationEntries(t, catalog, root, "metadata", "", 10)
	testutil.FailErr(t, "read metadata alias", err)
	if len(entries) != 1 || entries[0].Path != "metadata/config" {
		t.Fatalf("metadata alias entries: %+v", entries)
	}
}

func TestIndexCompletionCannotRetireAReplacementObservation(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "present.txt", "source")
	observed, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe directory", err)
	walk := discoveryWalk(t, catalog, root)
	writeIndexFile(t, root.Path, "replacement.txt", "new membership")
	walk.store.mu.Lock()
	walk.store.invalidateObservationsLocked(nil)
	walk.store.mu.Unlock()
	_, err = catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "replace observation", err)
	testutil.FailErr(t, "finish stale admission", walk.completeDir(t.Context(), ".", observed.Sequence))
	var count int
	testutil.FailErr(t, "read frontier", walk.tx.QueryRowContext(t.Context(), "SELECT count(*) FROM frontier WHERE path='.'").Scan(&count))
	if count != 1 {
		t.Fatal("stale admission retired the newer frontier")
	}
}

func TestRefreshingVisibleDirectoryPreservesRanks(t *testing.T) {
	catalog, root := indexFixture(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeIndexFile(t, root.Path, "src/"+name, "content")
	}
	_, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe root", err)
	observation, err := catalog.ObserveDirectory(t.Context(), "p", root, "src", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe visible directory", err)
	extent := func() int64 {
		navigation, err := catalog.OpenNavigation(t.Context(), "p", root)
		testutil.FailErr(t, "open navigation", err)
		defer func() { _ = navigation.Close() }()
		children, err := navigation.Children(t.Context(), ".")
		testutil.FailErr(t, "read root children", err)
		count, err := children.Extent(t.Context())
		testutil.FailErr(t, "read visible extent", err)
		return count
	}
	before := extent()
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open catalog store", err)
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "pin observation generation", err)
	defer pin.Release()
	builder, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create refresh builder", err)
	defer builder.close()
	observation.baseSequence, observation.Sequence = observation.Sequence, 0
	observation.Complete = false
	testutil.FailErr(t, "prepare refresh in progress", builder.observe(t.Context(), directoryDiscovery{observation: observation}))
	if during := extent(); during != before {
		t.Fatalf("unpublished refresh changed visible extent: %d -> %d", before, during)
	}
	observation.Complete = true
	nodes := make([]indexNode, 0, 3)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		nodes = append(nodes, indexNode{path: "src/" + name, parent: "src", name: name, regular: true})
	}
	testutil.FailErr(t, "complete directory refresh", builder.observe(t.Context(), directoryDiscovery{nodes: nodes, observation: observation}))
	testutil.FailErr(t, "finalize directory refresh", builder.finalize(t.Context()))
	testutil.FailErr(t, "publish directory refresh", store.publishStructure(t.Context(), builder, pin.Generation))
	if after := extent(); after != before {
		t.Fatalf("refresh completion changed visible extent: %d -> %d", before, after)
	}
}

func navigationEntries(t *testing.T, catalog *Catalog, root Root, dir, after string, limit int) ([]Entry, DirectoryState, error) {
	t.Helper()
	nav, err := catalog.OpenNavigation(t.Context(), "p", root)
	if err != nil {
		return nil, DirectoryState{}, err
	}
	defer func() { _ = nav.Close() }()
	children, err := nav.Children(t.Context(), dir)
	if err != nil {
		return nil, DirectoryState{}, err
	}
	state, err := nav.State(t.Context(), dir)
	if err != nil {
		return nil, DirectoryState{}, err
	}
	items, err := children.ReadAfter(t.Context(), after, limit)
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		entries = append(entries, nav.entryOf(item))
	}
	return entries, state, err
}

func TestContentOnlyInvalidationKeepsDirectoryObservation(t *testing.T) {
	catalog, root := indexFixture(t)
	t.Cleanup(repochange.ResetWatchersForTest)
	repochange.MarkCoverageCompleteForTest(root.Path)
	writeIndexFile(t, root.Path, "file.txt", "before")
	observed, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe root", err)
	writeIndexFile(t, root.Path, "file.txt", "after")
	repochange.Advance(root.Path)
	catalog.InvalidateRootChange(root.Path, []string{"file.txt"}, repochange.StructuralPathSet{})
	repeated, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "reuse after content edit", err)
	if repeated.Sequence != observed.Sequence {
		t.Fatalf("content-only invalidation rebuilt directory: before=%d after=%d", observed.Sequence, repeated.Sequence)
	}
}

func TestStructuralInvalidationRefreshesDirectoryObservation(t *testing.T) {
	catalog, root := indexFixture(t)
	t.Cleanup(repochange.ResetWatchersForTest)
	repochange.MarkCoverageCompleteForTest(root.Path)
	observed, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe empty root", err)
	writeIndexFile(t, root.Path, "created.txt", "new")
	repochange.Advance(root.Path)
	catalog.InvalidateRootChange(root.Path, []string{"created.txt"}, repochange.StructuralPathSet{Paths: []string{"created.txt"}})
	refreshed, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "refresh after create", err)
	if refreshed.Sequence == observed.Sequence {
		t.Fatal("structural invalidation reused stale directory observation")
	}
	entries, _, err := navigationEntries(t, catalog, root, ".", "", 10)
	testutil.FailErr(t, "read refreshed entries", err)
	if len(entries) != 1 || entries[0].Name != "created.txt" {
		t.Fatalf("entries after structural invalidation = %+v", entries)
	}
}
