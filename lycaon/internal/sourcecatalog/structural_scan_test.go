package sourcecatalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralScanWalksHiddenAndGitDirectories(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, ".hidden/file.txt", "hidden")
	writeIndexFile(t, root.Path, ".git/config", "git")
	writeIndexFile(t, root.Path, "src/main.go", "source")
	seen := collectStructuralScan(t, root, structuralScanOptions{})
	for _, want := range []string{".hidden", ".hidden/file.txt", ".git", ".git/config", "src", "src/main.go"} {
		if !slices.Contains(seen.paths, want) {
			t.Fatalf("scan did not include %q: %v", want, seen.paths)
		}
	}
	if !slices.Contains(seen.directories, ".") {
		t.Fatalf("scan did not include root directory: %v", seen.directories)
	}
}

func TestStructuralScanDoesNotDescendDirectorySymlink(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "target/inside.txt", "target")
	testutil.FailErr(t, "create directory symlink", os.Symlink("target", filepath.Join(root.Path, "link")))
	seen := collectStructuralScan(t, root, structuralScanOptions{})
	if !slices.Contains(seen.paths, "link") {
		t.Fatalf("scan did not include symlink: %v", seen.paths)
	}
	if slices.Contains(seen.directories, "link") {
		t.Fatalf("scan descended directory symlink: directories=%v", seen.directories)
	}
	if !slices.Contains(seen.paths, "target/inside.txt") {
		t.Fatalf("scan missed real target directory: %v", seen.paths)
	}
}

func TestStructuralScanSkipsPrivateTrees(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "visible.txt", "visible")
	private := filepath.Join(root.Path, "private")
	writeIndexFile(t, root.Path, "private/secret.txt", "secret")
	hold := repochange.HoldPrivateTree(private)
	t.Cleanup(hold)
	seen := collectStructuralScan(t, root, structuralScanOptions{})
	if slices.Contains(seen.paths, "private") || slices.Contains(seen.paths, "private/secret.txt") {
		t.Fatalf("scan included private paths: %v", seen.paths)
	}
	if !slices.Contains(seen.paths, "visible.txt") {
		t.Fatalf("scan missed visible file: %v", seen.paths)
	}
}

func TestStructuralScanChunksWideDirectoryWithCumulativeEntries(t *testing.T) {
	_, root := indexFixture(t)
	const entries = indexBatchSize + 17
	for i := range entries {
		writeIndexFile(t, root.Path, fmt.Sprintf("file-%03d.txt", i), "source")
	}
	handle := openRootForScan(t, root)
	var chunks []directoryDiscovery
	err := scanStructure(t.Context(), handle, ".", structuralScanOptions{workers: 1}, func(listing directoryDiscovery) error {
		if listing.observation.Path == "." {
			chunks = append(chunks, listing)
		}
		return nil
	})
	testutil.FailErr(t, "scan wide directory", err)
	if len(chunks) != 2 {
		t.Fatalf("chunks=%d, want 2", len(chunks))
	}
	if chunks[0].observation.Complete || chunks[0].observation.Entries != indexBatchSize {
		t.Fatalf("first chunk observation=%+v", chunks[0].observation)
	}
	if !chunks[1].observation.Complete || chunks[1].observation.Entries != entries {
		t.Fatalf("final chunk observation=%+v", chunks[1].observation)
	}
	if len(chunks[0].nodes) != indexBatchSize || len(chunks[1].nodes) != entries-indexBatchSize {
		t.Fatalf("node chunks=%d/%d", len(chunks[0].nodes), len(chunks[1].nodes))
	}
}

func TestStructuralScanEmitsFailureForUnreadableChild(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "changed/file.txt", "source")
	handle := openRootForScan(t, root)
	var failure DirectoryObservation
	marks := map[string]uint64{}
	options := structuralScanOptions{workers: 1, mark: func(dir string) uint64 {
		marks[dir]++
		return marks[dir]
	}}
	err := scanStructure(t.Context(), handle, ".", options, func(listing directoryDiscovery) error {
		if listing.observation.Path == "." {
			testutil.FailErr(t, "replace child directory", os.RemoveAll(filepath.Join(root.Path, "changed")))
			writeIndexFile(t, root.Path, "changed", "replacement")
		}
		if listing.observation.Path == "changed" {
			failure = listing.observation
		}
		return nil
	})
	testutil.FailErr(t, "scan with replaced child", err)
	if failure.Path != "changed" || failure.Failure == "" {
		t.Fatalf("child failure observation=%+v", failure)
	}
	if failure.Invalidation != 1 || marks["changed"] != 1 {
		t.Fatalf("failure did not retain the pre-open mark: observation=%+v marks=%v", failure, marks)
	}
}

func TestStructuralScanDoesNotFollowSwappedAncestorSymlink(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "a/child/file.txt", "source")
	writeIndexFile(t, root.Path, "private/child/secret.txt", "secret")
	private := filepath.Join(root.Path, "private")
	hold := repochange.HoldPrivateTree(private)
	t.Cleanup(hold)
	handle := openRootForScan(t, root)
	var failures []string
	var paths []string
	err := scanStructure(t.Context(), handle, ".", structuralScanOptions{workers: 1}, func(listing directoryDiscovery) error {
		if listing.observation.Path == "a" {
			testutil.FailErr(t, "rename ancestor directory", os.Rename(filepath.Join(root.Path, "a"), filepath.Join(root.Path, "old-a")))
			testutil.FailErr(t, "replace ancestor with symlink", os.Symlink("private", filepath.Join(root.Path, "a")))
		}
		if listing.observation.Failure != "" {
			failures = append(failures, listing.observation.Path)
		}
		for _, node := range listing.nodes {
			paths = append(paths, node.path)
		}
		return nil
	})
	testutil.FailErr(t, "scan with swapped ancestor symlink", err)
	if !slices.Contains(failures, "a/child") {
		t.Fatalf("scan did not report failure for queued child after ancestor symlink swap: failures=%v paths=%v", failures, paths)
	}
	if slices.Contains(paths, "a/child/file.txt") {
		t.Fatalf("scan used stale ancestor handle after path rename: %v", paths)
	}
	if slices.Contains(paths, "a/child/secret.txt") {
		t.Fatalf("scan followed swapped ancestor symlink into private tree: %v", paths)
	}
}

func TestStructuralScanFallsBackWhenCachedParentCannotOpenChild(t *testing.T) {
	if !structuralScanDirectoryCacheSupported() {
		t.Skip("structural scan directory cache unsupported on this platform")
	}
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "a/child/stale.txt", "stale")
	handle := openRootForScan(t, root)
	var failures []string
	var paths []string
	err := scanStructure(t.Context(), handle, ".", structuralScanOptions{workers: 1}, func(listing directoryDiscovery) error {
		if listing.observation.Path == "a" {
			testutil.FailErr(t, "rename cached parent", os.Rename(filepath.Join(root.Path, "a"), filepath.Join(root.Path, "old-a")))
			testutil.FailErr(t, "remove stale child", os.RemoveAll(filepath.Join(root.Path, "old-a", "child")))
			writeIndexFile(t, root.Path, "a/child/current.txt", "current")
		}
		if listing.observation.Failure != "" {
			failures = append(failures, listing.observation.Path)
		}
		for _, node := range listing.nodes {
			paths = append(paths, node.path)
		}
		return nil
	})
	testutil.FailErr(t, "scan after cached parent child miss", err)
	if slices.Contains(failures, "a/child") {
		t.Fatalf("cached parent child miss became visible failure: failures=%v paths=%v", failures, paths)
	}
	if !slices.Contains(paths, "a/child/current.txt") {
		t.Fatalf("scan did not fall back to current child path: failures=%v paths=%v", failures, paths)
	}
	if slices.Contains(paths, "a/child/stale.txt") {
		t.Fatalf("scan used stale cached parent contents: %v", paths)
	}
}

func TestStructuralScanDescendFilterStopsOnlyChildRecursion(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "a/file.txt", "a")
	writeIndexFile(t, root.Path, "b/file.txt", "b")
	seen := collectStructuralScan(t, root, structuralScanOptions{descend: func(dir string) (bool, error) { return dir == "a", nil }})
	for _, want := range []string{"a", "a/file.txt", "b"} {
		if !slices.Contains(seen.paths, want) {
			t.Fatalf("scan missing %q with descend filter: paths=%v directories=%v", want, seen.paths, seen.directories)
		}
	}
	if slices.Contains(seen.paths, "b/file.txt") || slices.Contains(seen.directories, "b") {
		t.Fatalf("scan descended filtered directory: paths=%v directories=%v", seen.paths, seen.directories)
	}
}

func TestStructuralScanCacheReleasesDescriptors(t *testing.T) {
	if !structuralScanDirectoryCacheSupported() {
		t.Skip("structural scan directory cache unsupported on this platform")
	}
	before := structuralScanCachedDescriptors.Load()
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "a/child/file.txt", "source")
	seen := collectStructuralScan(t, root, structuralScanOptions{workers: 1})
	if !slices.Contains(seen.paths, "a/child/file.txt") {
		t.Fatalf("scan missed cached descendant: %v", seen.paths)
	}
	if got := structuralScanCachedDescriptors.Load(); got != before {
		t.Fatalf("cached descriptors after scan=%d, want %d", got, before)
	}
}

func TestStructuralScanDirectoryCacheBoundsAndPinsEvictedEntries(t *testing.T) {
	if !structuralScanDirectoryCacheSupported() {
		t.Skip("structural scan directory cache unsupported on this platform")
	}
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "a/child/file.txt", "a")
	writeIndexFile(t, root.Path, "b/child/file.txt", "b")
	handle := openRootForScan(t, root)
	before := structuralScanCachedDescriptors.Load()
	cache := newStructuralScanDirectoryCache(handle, 1)
	defer cache.close()
	rootDirectory, err := openStructuralScanDirectoryUnadmitted(handle, ".", nil)
	testutil.FailErr(t, "open root directory", err)
	defer rootDirectory.close()
	cache.remember(".", rootDirectory.handle)
	held := cache.acquire(".")
	if held == nil {
		t.Fatal("cache did not retain root entry")
	}
	aDirectory, err := openStructuralScanDirectoryUnadmitted(handle, "a", nil)
	testutil.FailErr(t, "open a directory", err)
	defer aDirectory.close()
	cache.remember("a", aDirectory.handle)
	if len(cache.entries) != 1 {
		t.Fatalf("cache entries=%d, want 1", len(cache.entries))
	}
	if got, want := structuralScanCachedDescriptors.Load(), before+3; got != want {
		t.Fatalf("cached descriptors while evicted entry is pinned=%d, want %d", got, want)
	}
	cache.release(held)
	if got, want := structuralScanCachedDescriptors.Load(), before+2; got != want {
		t.Fatalf("cached descriptors after releasing evicted entry=%d, want %d", got, want)
	}
}

func TestStructuralScanQueueSpoolsOverflow(t *testing.T) {
	spoolDir := t.TempDir()
	queue := structuralScanQueue{dir: spoolDir}
	t.Cleanup(queue.close)
	paths := make([]string, maxStructuralScanQueuedDirs+3)
	for i := range paths {
		paths[i] = fmt.Sprintf("dir-%05d", i)
	}
	testutil.FailErr(t, "queue paths", queue.push(paths))
	if queue.spool == nil || queue.spool.count == 0 {
		t.Fatalf("queue did not spool overflow: memory=%d spool=%v", len(queue.memory), queue.spool)
	}
	var popped int
	for queue.len() > 0 {
		_, err := queue.pop()
		testutil.FailErr(t, "pop queued path", err)
		popped++
	}
	if popped != len(paths) {
		t.Fatalf("popped=%d, want %d", popped, len(paths))
	}
	if queue.spool == nil {
		t.Fatal("spool disappeared before cleanup")
	}
	if queue.spool.file.Name() == "" {
		t.Fatal("spool file name was empty")
	}
}

func TestStructuralScanCacheReplacesEntriesAtProcessCapacity(t *testing.T) {
	if !structuralScanDirectoryCacheSupported() {
		t.Skip("directory handle caching is unavailable on this platform")
	}
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "next/file.txt", "source")
	handle := openRootForScan(t, root)
	cache := newStructuralScanDirectoryCache(handle, 2)
	defer cache.close()
	directory, err := openStructuralScanDirectoryUnadmitted(handle, ".", nil)
	testutil.FailErr(t, "open cached parent", err)
	defer directory.close()
	cache.remember(".", directory.handle)
	reserved := 0
	for reserveStructuralScanCachedDescriptor() {
		reserved++
	}
	defer func() {
		for range reserved {
			releaseStructuralScanCachedDescriptor()
		}
	}()
	next, err := openStructuralScanDirectoryUnadmitted(handle, "next", nil)
	testutil.FailErr(t, "open replacement parent", err)
	defer next.close()
	cache.remember("next", next.handle)
	if cache.entries["next"] == nil || cache.entries["."] != nil {
		t.Fatal("process capacity froze the cache instead of replacing an old entry")
	}
	if got := structuralScanCachedDescriptors.Load(); got != maxStructuralScanCachedDescriptors {
		t.Fatalf("cached descriptor count = %d at capacity", got)
	}
}

func TestStructuralScanQueueReclaimsSpoolTailOnChurn(t *testing.T) {
	queue := structuralScanQueue{dir: t.TempDir(), bytes: maxStructuralScanQueuedBytes}
	t.Cleanup(queue.close)
	testutil.FailErr(t, "seed spool", queue.push([]string{"dir-000"}))
	if queue.spool == nil {
		t.Fatal("queue did not create spool")
	}
	liveBytes := queue.spool.bytes
	for i := range 200 {
		_, err := queue.pop()
		testutil.FailErr(t, "pop spooled path", err)
		testutil.FailErr(t, "push spooled path", queue.push([]string{fmt.Sprintf("dir-%03d", i)}))
		info, err := queue.spool.file.Stat()
		testutil.FailErr(t, "stat spool file", err)
		if info.Size() != queue.spool.bytes {
			t.Fatalf("spool physical size=%d, logical bytes=%d", info.Size(), queue.spool.bytes)
		}
		if queue.spool.bytes != liveBytes {
			t.Fatalf("spool bytes after churn=%d, want %d", queue.spool.bytes, liveBytes)
		}
	}
}

func TestStructuralScanQueueInitialPathCountsTowardByteBudget(t *testing.T) {
	queue := structuralScanQueue{dir: t.TempDir()}
	t.Cleanup(queue.close)
	testutil.FailErr(t, "queue start path", queue.push([]string{"start"}))
	if queue.bytes != structuralScanQueueEntrySize("start") {
		t.Fatalf("initial bytes=%d, want %d", queue.bytes, structuralScanQueueEntrySize("start"))
	}
	queue.bytes = maxStructuralScanQueuedBytes
	testutil.FailErr(t, "queue spill path", queue.push([]string{"next"}))
	if queue.spool == nil || queue.spool.count != 1 {
		t.Fatalf("overflow path did not spill after initial byte accounting: %+v", queue.spool)
	}
}

func TestStructuralScanQueueReclaimsSpoolStorage(t *testing.T) {
	queue := structuralScanQueue{dir: t.TempDir()}
	t.Cleanup(queue.close)
	for i := range maxStructuralScanQueuedDirs + 4 {
		if err := queue.push([]string{fmt.Sprintf("dir-%05d", i)}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	if queue.spool == nil || queue.spool.count == 0 {
		t.Fatal("fixture did not spill queued directories")
	}
	for queue.len() > 0 {
		previousCount, previousBytes := queue.spoolCount(), queue.spool.bytes
		if _, err := queue.pop(); err != nil {
			t.Fatalf("pop spilled directory: %v", err)
		}
		info, err := queue.spool.file.Stat()
		testutil.FailErr(t, "stat scan spool", err)
		if info.Size() != queue.spool.bytes {
			t.Fatalf("spool bytes = %d file = %d", queue.spool.bytes, info.Size())
		}
		if queue.spoolCount() < previousCount && queue.spool.bytes >= previousBytes {
			t.Fatalf("consumed spool did not shrink: previous = %d current = %d", previousBytes, queue.spool.bytes)
		}
	}
	if queue.spool.bytes != 0 {
		t.Fatalf("empty queue retained %d spool bytes", queue.spool.bytes)
	}
}

func TestStructuralScanStopsOnEmitError(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "file.txt", "source")
	want := errors.New("stop scan")
	handle := openRootForScan(t, root)
	err := scanStructure(t.Context(), handle, ".", structuralScanOptions{}, func(directoryDiscovery) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("scan error=%v, want %v", err, want)
	}
}

func BenchmarkStructuralScan(b *testing.B) {
	rootPath := os.Getenv("PW_TREE_BENCH_ROOT")
	if rootPath == "" {
		b.Skip("set PW_TREE_BENCH_ROOT to benchmark structural scanning")
	}
	var totalDirectories int64
	var totalEntries int64
	b.ResetTimer()
	for range b.N {
		handle, err := os.OpenRoot(rootPath)
		if err != nil {
			b.Fatalf("open benchmark root: %v", err)
		}
		var directories int64
		var entries int64
		err = scanStructure(b.Context(), handle, ".", structuralScanOptions{}, func(listing directoryDiscovery) error {
			entries += int64(len(listing.nodes))
			if listing.observation.Complete {
				directories++
			}
			return nil
		})
		if closeErr := handle.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			b.Fatalf("scan benchmark root: %v", err)
		}
		totalDirectories += directories
		totalEntries += entries
	}
	b.ReportMetric(float64(totalDirectories)/float64(b.N), "dirs/op")
	b.ReportMetric(float64(totalEntries)/float64(b.N), "entries/op")
}

type structuralScanSeen struct {
	paths       []string
	directories []string
}

func collectStructuralScan(t *testing.T, root Root, options structuralScanOptions) structuralScanSeen {
	t.Helper()
	handle := openRootForScan(t, root)
	seen := structuralScanSeen{}
	err := scanStructure(t.Context(), handle, ".", options, func(listing directoryDiscovery) error {
		seen.directories = append(seen.directories, listing.observation.Path)
		for _, node := range listing.nodes {
			seen.paths = append(seen.paths, node.path)
		}
		return nil
	})
	testutil.FailErr(t, "scan structure", err)
	slices.Sort(seen.paths)
	slices.Sort(seen.directories)
	return seen
}

func openRootForScan(t *testing.T, root Root) *os.Root {
	t.Helper()
	handle, err := os.OpenRoot(root.Path)
	testutil.FailErr(t, "open root", err)
	t.Cleanup(func() { testutil.FailErr(t, "close root", handle.Close()) })
	return handle
}

func TestStructuralScanPropagatesDescendStorageFailure(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "child/file.txt", "source")
	failure := errors.New("directory metadata unavailable")
	err := scanStructure(t.Context(), openRootForScan(t, root), ".", structuralScanOptions{
		descend: func(string) (bool, error) { return false, failure },
	}, func(directoryDiscovery) error { return nil })
	if !errors.Is(err, failure) || !errors.Is(err, errStructuralScanControl) {
		t.Fatalf("storage failure was converted into directory coverage: %v", err)
	}
}
