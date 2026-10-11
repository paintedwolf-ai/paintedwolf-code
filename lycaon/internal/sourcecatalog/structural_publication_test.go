package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralPublicationReleasesWritersBeforeNotifyingReaders(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get publication store", err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, resume := make(chan struct{}), make(chan struct{})
	unsubscribe := catalog.Directories.SubscribeNavigation(root, func() {
		close(entered)
		<-resume
	})
	defer unsubscribe()
	done := make(chan error, 1)
	go func() { done <- store.buildStructure(ctx, map[string]struct{}{".": {}}, true) }()
	defer func() {
		close(resume)
		testutil.FailErr(t, "finish publication notification", <-done)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("publication did not reach its readers")
	}
	release, err := store.acquireStructurePublication(ctx)
	testutil.FailErr(t, "admit writer while reader notification waits", err)
	release()
}

func TestStructuralTerminalFailureSettlesKnownUnresolvedChildren(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "failed/unknown/file.txt", "source")
	for _, dir := range []string{".", "failed"} {
		_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{})
		testutil.FailErr(t, "observe known parent", err)
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get failure store", err)
	complete, err := store.subtreeComplete(t.Context(), ".")
	testutil.FailErr(t, "read pending subtree coverage", err)
	if complete {
		t.Fatal("unobserved descendant did not require preparation")
	}
	failed, err := store.readObservation(t.Context(), "failed")
	testutil.FailErr(t, "capture failed listing basis", err)
	failed.baseSequence = failed.Sequence
	testutil.FailErr(t, "publish terminal listing failure", store.recordObservationFailure(t.Context(), failed, errors.New("directory unavailable")))
	for _, dir := range []string{".", "failed"} {
		complete, err := store.subtreeComplete(t.Context(), dir)
		testutil.FailErr(t, "read settled failure coverage", err)
		if !complete {
			t.Fatalf("terminal failure left %q preparation unresolved", dir)
		}
	}
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open failed generation", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "failed/unknown")
	testutil.FailErr(t, "retain known child after failure", err)
	state, err := navigation.State(t.Context(), "failed")
	testutil.FailErr(t, "read terminal directory state", err)
	if state.Failure == "" || state.Complete {
		t.Fatalf("terminal state = %+v", state)
	}
}

func TestStructuralRebasePreservesForegroundRevalidation(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "same/unchanged.txt", "source")
	writeIndexFile(t, root.Path, "other/original.txt", "source")
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get rebase store", err)
	testutil.FailErr(t, "build initial membership", store.buildStructure(t.Context(), map[string]struct{}{".": {}}, true))
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain bulk scan basis", err)
	defer pin.Release()
	bulk, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create bulk builder", err)
	defer bulk.close()
	observePublicationFixture(t, bulk, store, "same", []string{"unchanged.txt"}, false)
	observePublicationFixture(t, bulk, store, "other", []string{"bulk.txt"}, false)
	testutil.FailErr(t, "finalize bulk observations", bulk.finalize(t.Context()))
	before, err := store.readObservation(t.Context(), "same")
	testutil.FailErr(t, "capture foreground basis", err)
	store.fenceObservationSubtree("same")
	foreground, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, "same", DirectoryRead{})
	testutil.FailErr(t, "refresh unchanged foreground membership", err)
	if foreground.Sequence != before.Sequence || foreground.Invalidation == before.Invalidation {
		t.Fatal("fixture did not preserve membership sequence across revalidation")
	}
	testutil.FailErr(t, "publish bulk beside foreground revalidation", store.publishStructure(t.Context(), bulk, pin.Generation))
	after, err := store.readObservation(t.Context(), "same")
	testutil.FailErr(t, "read retained foreground observation", err)
	if after != foreground {
		t.Fatalf("bulk replaced foreground observation: got %+v, want %+v", after, foreground)
	}
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open merged generation", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "other/bulk.txt")
	testutil.FailErr(t, "retain independent bulk change", err)
}

func TestStructuralRebaseDoesNotResurrectForegroundRemovedSubtree(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get removal store", err)
	testutil.FailErr(t, "publish empty root", store.buildStructure(t.Context(), map[string]struct{}{".": {}}, true))
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain pre-scan generation", err)
	defer pin.Release()
	bulk, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create stale membership builder", err)
	defer bulk.close()
	writeIndexFile(t, root.Path, "transient/file.txt", "temporary")
	observePublicationFixture(t, bulk, store, "transient", []string{"file.txt"}, false)
	testutil.FailErr(t, "finalize transient membership", bulk.finalize(t.Context()))
	testutil.FailErr(t, "remove transient directory", os.RemoveAll(filepath.Join(root.Path, "transient")))
	store.fenceObservationSubtree(".")
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "publish foreground removal", err)
	testutil.FailErr(t, "rebase stale transient scan", store.publishStructure(t.Context(), bulk, pin.Generation))
	if _, err := store.readObservation(t.Context(), "transient"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("removed subtree observation resurrected: %v", err)
	}
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open post-removal generation", err)
	defer func() { _ = navigation.Close() }()
	if _, err := navigation.Entry(t.Context(), "transient"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("removed directory reappeared: %v", err)
	}
}

func observePublicationFixture(t *testing.T, builder *structuralBuilder, store *indexStore, dir string, names []string, directories bool) {
	t.Helper()
	nodes := make([]indexNode, len(names))
	for i, name := range names {
		nodes[i] = indexNode{path: path.Join(dir, name), parent: dir, name: name, isDir: directories, regular: !directories}
	}
	observation := DirectoryObservation{Path: dir, Entries: len(nodes), Complete: true, Invalidation: store.observationMark(dir), Epoch: repochange.CurrentEpoch(store.root.Path)}
	testutil.FailErr(t, "observe publication fixture", builder.observe(t.Context(), directoryDiscovery{nodes: nodes, observation: observation}))
}

func TestForegroundPublicationCompletesWhileBulkCoverageWaits(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "bulk/original.txt", "source")
	writeIndexFile(t, root.Path, "foreground/original.txt", "source")
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get publication store", err)
	testutil.FailErr(t, "build initial structure", store.buildStructure(t.Context(), map[string]struct{}{".": {}}, true))
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain bulk basis", err)
	defer pin.Release()
	bulk, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create bulk builder", err)
	defer bulk.close()
	observePublicationFixture(t, bulk, store, "bulk", []string{"new.txt", "original.txt"}, false)
	coverageStarted := make(chan struct{})
	coverageRelease := make(chan struct{})
	var coverageStartedOnce sync.Once
	bulk.coverage = func(ctx context.Context, target *structuralBuilder) error {
		coverageStartedOnce.Do(func() { close(coverageStarted) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-coverageRelease:
			return nil
		}
	}
	bulkDone := make(chan error, 1)
	go func() { bulkDone <- store.publishStructure(t.Context(), bulk, pin.Generation) }()
	<-coverageStarted
	writeIndexFile(t, root.Path, "foreground/new.txt", "source")
	store.fenceObservationSubtree("foreground")
	foreground, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, "foreground", DirectoryRead{})
	testutil.FailErr(t, "publish foreground while bulk coverage waits", err)
	if !foreground.Complete || foreground.Entries != 2 {
		t.Fatalf("foreground observation = %+v", foreground)
	}
	close(coverageRelease)
	testutil.FailErr(t, "publish bulk after foreground", <-bulkDone)
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open merged navigation", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "bulk/new.txt")
	testutil.FailErr(t, "retain bulk entry", err)
	_, err = navigation.Entry(t.Context(), "foreground/new.txt")
	testutil.FailErr(t, "retain foreground entry", err)
}

func TestStructuralPublicationMergePreservesPreparedParentMembership(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "a/original.txt", "source")
	writeIndexFile(t, root.Path, "z/original.txt", "source")
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get publication store", err)
	testutil.FailErr(t, "build initial structure", store.buildStructure(t.Context(), map[string]struct{}{".": {}}, true))
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain bulk basis", err)
	defer pin.Release()
	bulk, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create bulk builder", err)
	defer bulk.close()
	observePublicationFixture(t, bulk, store, ".", []string{"a", "new-parent", "z"}, true)
	observePublicationFixture(t, bulk, store, "new-parent", []string{"file.txt"}, false)
	coverageStarted := make(chan struct{})
	coverageRelease := make(chan struct{})
	var coverageStartedOnce sync.Once
	bulk.coverage = func(ctx context.Context, target *structuralBuilder) error {
		coverageStartedOnce.Do(func() { close(coverageStarted) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-coverageRelease:
			return nil
		}
	}
	bulkDone := make(chan error, 1)
	go func() { bulkDone <- store.publishStructure(t.Context(), bulk, pin.Generation) }()
	<-coverageStarted
	writeIndexFile(t, root.Path, "a/foreground.txt", "source")
	store.fenceObservationSubtree("a")
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, "a", DirectoryRead{})
	testutil.FailErr(t, "publish foreground child repair", err)
	close(coverageRelease)
	testutil.FailErr(t, "publish bulk parent membership", <-bulkDone)
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open merged navigation", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "new-parent/file.txt")
	testutil.FailErr(t, "retain prepared parent membership", err)
	_, err = navigation.Entry(t.Context(), "a/foreground.txt")
	testutil.FailErr(t, "retain foreground child repair", err)
}

func TestStructuralPublicationMergeKeepsCompletePreparedListingOverPartialForeground(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "target/actual-a.txt", "source")
	writeIndexFile(t, root.Path, "target/actual-b.txt", "source")
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get publication store", err)
	testutil.FailErr(t, "build initial structure", store.buildStructure(t.Context(), map[string]struct{}{".": {}}, true))
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain bulk basis", err)
	defer pin.Release()
	bulk, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create bulk builder", err)
	defer bulk.close()
	observePublicationFixture(t, bulk, store, "target", []string{"actual-a.txt", "actual-b.txt", "bulk-only.txt"}, false)
	coverageStarted := make(chan struct{})
	coverageRelease := make(chan struct{})
	var coverageStartedOnce sync.Once
	bulk.coverage = func(ctx context.Context, target *structuralBuilder) error {
		coverageStartedOnce.Do(func() { close(coverageStarted) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-coverageRelease:
			return nil
		}
	}
	bulkDone := make(chan error, 1)
	go func() { bulkDone <- store.publishStructure(t.Context(), bulk, pin.Generation) }()
	<-coverageStarted
	store.fenceObservationSubtree("target")
	partial, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, "target", DirectoryRead{Entries: 1})
	testutil.FailErr(t, "publish partial foreground listing", err)
	if partial.Complete {
		t.Fatal("foreground fixture unexpectedly completed")
	}
	close(coverageRelease)
	testutil.FailErr(t, "publish bulk over partial foreground", <-bulkDone)
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open merged navigation", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "target/bulk-only.txt")
	testutil.FailErr(t, "retain complete prepared listing", err)
}

func TestStructuralPublicationSurvivesSuccessiveForegroundHeads(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "bulk/original.txt", "source")
	writeIndexFile(t, root.Path, "foreground-one/original.txt", "source")
	writeIndexFile(t, root.Path, "foreground-two/original.txt", "source")
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get publication store", err)
	testutil.FailErr(t, "build initial structure", store.buildStructure(t.Context(), map[string]struct{}{".": {}}, true))
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain bulk basis", err)
	defer pin.Release()
	bulk, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create bulk builder", err)
	defer bulk.close()
	observePublicationFixture(t, bulk, store, "bulk", []string{"bulk-new.txt", "original.txt"}, false)
	coverage := publicationCoverageGate{
		started:  []chan struct{}{make(chan struct{}), make(chan struct{})},
		released: []chan struct{}{make(chan struct{}), make(chan struct{})},
	}
	bulk.coverage = coverage.wait
	bulkDone := make(chan error, 1)
	go func() { bulkDone <- store.publishStructure(t.Context(), bulk, pin.Generation) }()
	<-coverage.started[0]
	writeIndexFile(t, root.Path, "foreground-one/new.txt", "source")
	store.fenceObservationSubtree("foreground-one")
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, "foreground-one", DirectoryRead{})
	testutil.FailErr(t, "publish first foreground head", err)
	close(coverage.released[0])
	<-coverage.started[1]
	if !coverage.reverseMerged.Load() {
		t.Fatal("coverage did not run on the reverse-merged builder")
	}
	const fallbackDirs = pagedview.PageFanout + 4
	for i := range fallbackDirs {
		writeIndexFile(t, root.Path, fmt.Sprintf("fallback-%03d/file.txt", i), "source")
	}
	store.fenceObservationSubtree(".")
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "publish partition-changing root head", err)
	for i := range fallbackDirs {
		dir := fmt.Sprintf("fallback-%03d", i)
		_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{})
		testutil.FailErr(t, "publish partition-changing child head", err)
	}
	close(coverage.released[1])
	testutil.FailErr(t, "publish bulk after partition fallback", <-bulkDone)
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open merged navigation", err)
	defer func() { _ = navigation.Close() }()
	for _, entry := range []string{"bulk/bulk-new.txt", "foreground-one/new.txt", "fallback-000/file.txt", fmt.Sprintf("fallback-%03d/file.txt", fallbackDirs-1)} {
		_, err := navigation.Entry(t.Context(), entry)
		testutil.FailErr(t, "retain "+entry, err)
	}
}

type publicationCoverageGate struct {
	calls         atomic.Int32
	reverseMerged atomic.Bool
	started       []chan struct{}
	released      []chan struct{}
}

func (gate *publicationCoverageGate) wait(ctx context.Context, target *structuralBuilder) error {
	call := int(gate.calls.Add(1)) - 1
	if target.reverseMerged {
		gate.reverseMerged.Store(true)
	}
	if call >= len(gate.started) {
		return nil
	}
	close(gate.started[call])
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate.released[call]:
		return nil
	}
}

func TestStructuralPublicationDiffDeclinesBranchPartitionChanges(t *testing.T) {
	pages := &directoryTestPages{}
	oldLeft := writeDirectoryDiffLeaf(t, pages, "a")
	oldRight := writeDirectoryDiffLeaf(t, pages, "z")
	oldRoot := writeDirectoryDiffBranches(t, pages, pagedview.Branch{Key: "a", Page: oldLeft}, pagedview.Branch{Key: "z", Page: oldRight})
	newLeft := writeDirectoryDiffLeaf(t, pages, "a")
	newMiddle := writeDirectoryDiffLeaf(t, pages, "m")
	newRight := writeDirectoryDiffLeaf(t, pages, "z")
	newRoot := writeDirectoryDiffBranches(t, pages, pagedview.Branch{Key: "a", Page: newLeft}, pagedview.Branch{Key: "m", Page: newMiddle}, pagedview.Branch{Key: "z", Page: newRight})
	oldIndex := structuralDirectoryIndex{root: oldRoot, count: 2, store: pages}
	newIndex := structuralDirectoryIndex{root: newRoot, count: 3, store: pages}
	err := visitDirectoryIndexDiff(t.Context(), oldIndex, newIndex, structuralPublicationMergeLimit, func(string, bool, structuralDirectory, bool, structuralDirectory) error { return nil })
	if !errors.Is(err, errStructuralMergeUnavailable) {
		t.Fatalf("partition-changing diff error = %v, want %v", err, errStructuralMergeUnavailable)
	}
}

func writeDirectoryDiffLeaf(t *testing.T, pages *directoryTestPages, key string) uint64 {
	t.Helper()
	id, err := pages.Write(t.Context(), 0, pagedview.RangePage[structuralDirectory]{Items: []pagedview.RangeItem[structuralDirectory]{{Key: key, Value: structuralDirectory{observation: DirectoryObservation{Path: key, Sequence: 1}}, Weight: 1}}})
	testutil.FailErr(t, "write diff leaf", err)
	return id
}

func writeDirectoryDiffBranches(t *testing.T, pages *directoryTestPages, branches ...pagedview.Branch) uint64 {
	t.Helper()
	id, err := pages.Write(t.Context(), 0, pagedview.RangePage[structuralDirectory]{Children: branches})
	testutil.FailErr(t, "write diff branches", err)
	return id
}
