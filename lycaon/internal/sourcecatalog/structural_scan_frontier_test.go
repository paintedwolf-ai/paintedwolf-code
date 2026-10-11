package sourcecatalog

import (
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralScanDefersDeclaredOutputAndVCSUntilSourceCompletes(t *testing.T) {
	for _, workers := range []int{1, maxStructuralScanWorkers} {
		t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
			_, root := indexFixture(t)
			writeIndexFile(t, root.Path, ".gitignore", "arbitrary-output/\n")
			writeIndexFile(t, root.Path, "module/.ignore", "derived/\nkept/\n!kept/\n")
			writeIndexFile(t, root.Path, ".git/info/exclude", "local-output/\n")
			ordinary := []string{".", "src", "src/deep", "module", "module/kept", ".hidden", "build-scripts"}
			deferred := []string{"arbitrary-output", "arbitrary-output/nested", "module/derived", "local-output", ".git", ".git/info", ".hg", "node_modules", "target", "build"}
			for _, dir := range append(slices.Clone(ordinary[1:]), deferred...) {
				writeIndexFile(t, root.Path, dir+"/file.txt", "content")
			}
			// Source readers remain active between batches even when their queue is empty.
			for i := range indexBatchSize + 1 {
				writeIndexFile(t, root.Path, fmt.Sprintf("src/entry-%03d.txt", i), "source")
			}
			completed := make(map[string]bool)
			err := scanStructure(t.Context(), openRootForScan(t, root), ".", structuralScanOptions{workers: workers}, func(listing directoryDiscovery) error {
				dir := listing.observation.Path
				if slices.Contains(deferred, dir) {
					for _, source := range ordinary {
						if !completed[source] {
							t.Errorf("deferred %q started before source %q completed", dir, source)
						}
					}
				}
				if listing.observation.Complete {
					completed[dir] = true
				}
				return nil
			})
			testutil.FailErr(t, "scan ordered structure", err)
			for _, dir := range append(ordinary, deferred...) {
				if !completed[dir] {
					t.Errorf("directory %q was excluded instead of deferred", dir)
				}
			}
		})
	}
}

func TestStructuralScanPriorityRefreshesIgnoreRules(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, ".ignore", "first/\n")
	store, err := c.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "create index store", err)
	options := structuralScanOptions{store: store}
	for _, ignored := range []string{"first", "second"} {
		writeIndexFile(t, root.Path, ".ignore", ignored+"/\n")
		frontier := newStructuralScanFrontier(t.Context(), root.Path, options)
		testutil.FailErr(t, "queue directories", frontier.push([]string{"first", "second"}))
		first, err := frontier.pop()
		testutil.FailErr(t, "pop ordinary directory", err)
		last, err := frontier.pop()
		testutil.FailErr(t, "pop ignored directory", err)
		frontier.close()
		if first == ignored || last != ignored {
			t.Fatalf("ignored %q: order %q, %q", ignored, first, last)
		}
	}
}

func TestStructuralScanPriorityHonorsDeviceSetting(t *testing.T) {
	c, root := indexFixture(t)
	cfg, err := sourcescope.DefaultConfig()
	testutil.FailErr(t, "load catalog policy", err)
	cfg.Catalog.DeferIgnored = false
	c.SetScopes(testScopes{plane: cfg.Catalog})
	writeIndexFile(t, root.Path, ".ignore", "output/\n")
	store, err := c.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "create index store", err)
	frontier := newStructuralScanFrontier(t.Context(), root.Path, structuralScanOptions{store: store})
	defer frontier.close()
	testutil.FailErr(t, "queue directories", frontier.push([]string{"source", "output", ".git"}))
	if frontier.queues[laneSource].len() != 2 || frontier.queues[laneCollapsed].len() != 1 {
		t.Fatalf("priority disabled: source=%d collapsed=%d", frontier.queues[laneSource].len(), frontier.queues[laneCollapsed].len())
	}
}

// A committed dependency tree is traversed late and still opened by a recursive
// expansion, so it waits behind first-party source but ahead of collapsed trees.
func TestStructuralScanFrontierOrdersExpandableVendoringBeforeCollapsedTrees(t *testing.T) {
	c, root := indexFixture(t)
	cfg, err := sourcescope.DefaultConfig()
	testutil.FailErr(t, "load catalog policy", err)
	c.SetScopes(testScopes{plane: cfg.Catalog})
	store, err := c.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "create index store", err)
	frontier := newStructuralScanFrontier(t.Context(), root.Path, structuralScanOptions{store: store})
	defer frontier.close()
	testutil.FailErr(t, "queue directories", frontier.push([]string{"third_party", "src", "node_modules"}))
	if frontier.queues[laneSource].len() != 1 || frontier.queues[laneLate].len() != 1 || frontier.queues[laneCollapsed].len() != 1 {
		t.Fatalf("lane split: source=%d late=%d collapsed=%d",
			frontier.queues[laneSource].len(), frontier.queues[laneLate].len(), frontier.queues[laneCollapsed].len())
	}
	if frontier.atLaneBoundary(0) {
		t.Fatal("the boundary fired while an opened tree was still queued")
	}
	var order []string
	for range 2 {
		next, err := frontier.pop()
		testutil.FailErr(t, "pop opened directory", err)
		order = append(order, next)
	}
	if !slices.Equal(order, []string{"src", "third_party"}) {
		t.Fatalf("walk order = %v, want first-party source before committed vendoring", order)
	}
	if !frontier.atLaneBoundary(0) {
		t.Fatal("the boundary did not fire once every opened tree was listed")
	}
}

func TestStructuralScanFrontierSpillsBothLanesAndWaitsForSourceReaders(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, ".ignore", "output/\n")
	frontier := newStructuralScanFrontier(t.Context(), root.Path, structuralScanOptions{spoolDir: t.TempDir()})
	defer frontier.close()
	frontier.queues[laneSource].bytes = maxStructuralScanQueuedBytes
	frontier.queues[laneCollapsed].bytes = maxStructuralScanQueuedBytes
	testutil.FailErr(t, "queue spillable directories", frontier.push([]string{"output", "src"}))
	if frontier.queues[laneSource].spoolCount() != 1 || frontier.queues[laneCollapsed].spoolCount() != 1 {
		t.Fatal("both priority lanes must spill under memory pressure")
	}
	first, err := frontier.pop()
	testutil.FailErr(t, "pop spilled source", err)
	if first != "src" || frontier.ready(1) || !frontier.ready(0) {
		t.Fatalf("source reader barrier failed after %q", first)
	}
	testutil.FailErr(t, "discover deeper source", frontier.push([]string{"src/deep"}))
	second, err := frontier.pop()
	testutil.FailErr(t, "pop deeper source", err)
	last, err := frontier.pop()
	testutil.FailErr(t, "pop spilled output", err)
	if second != "src/deep" || last != "output" || frontier.len() != 0 {
		t.Fatalf("spilled walk order: %q, %q, %q; remaining=%d", first, second, last, frontier.len())
	}
	testutil.FailErr(t, "queue output descendant", frontier.push([]string{"output/deep"}))
	if !frontier.ready(1) || frontier.queues[laneSource].len() != 0 {
		t.Fatal("collapsed descendants must stay in the collapsed lane and run concurrently")
	}
}
