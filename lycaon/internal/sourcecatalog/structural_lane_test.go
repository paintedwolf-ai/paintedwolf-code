package sourcecatalog

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralScanRunsLaneBoundaryOnceBetweenLanes(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, ".gitignore", "output/\n")
	for _, dir := range []string{"src", "src/deep", "output", "output/nested"} {
		writeIndexFile(t, root.Path, dir+"/file.txt", "content")
	}
	var listed []string
	var atBoundary []string
	boundaries := 0
	options := structuralScanOptions{laneBoundary: func(context.Context) error {
		boundaries++
		atBoundary = slices.Clone(listed)
		return nil
	}}
	err := scanStructure(t.Context(), openRootForScan(t, root), ".", options, func(listing directoryDiscovery) error {
		if listing.observation.Complete {
			listed = append(listed, listing.observation.Path)
		}
		return nil
	})
	testutil.FailErr(t, "scan ordered structure", err)
	if boundaries != 1 {
		t.Fatalf("lane boundary ran %d times", boundaries)
	}
	slices.Sort(atBoundary)
	if !slices.Equal(atBoundary, []string{".", "src", "src/deep"}) {
		t.Fatalf("directories listed at the lane boundary = %v", atBoundary)
	}
	slices.Sort(listed)
	if !slices.Equal(listed, []string{".", "output", "output/nested", "src", "src/deep"}) {
		t.Fatalf("directories listed by the whole scan = %v", listed)
	}
}

func TestStructuralScanSkipsLaneBoundaryWithoutCollapsedDirectories(t *testing.T) {
	_, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/file.txt", "content")
	boundaries := 0
	options := structuralScanOptions{laneBoundary: func(context.Context) error { boundaries++; return nil }}
	err := scanStructure(t.Context(), openRootForScan(t, root), ".", options, func(directoryDiscovery) error { return nil })
	testutil.FailErr(t, "scan plain structure", err)
	if boundaries != 0 {
		t.Fatalf("lane boundary ran %d times with nothing collapsed", boundaries)
	}
}

// laneStates records, at every publication, whether each directory is complete.
// Publications arrive on the inventory goroutine, so failures are kept for the
// test goroutine to report.
type laneStates struct {
	mu   sync.Mutex
	seen [][]bool
	errs []error
}

func (s *laneStates) record(ctx context.Context, c *Catalog, root Root, dirs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	navigation, err := c.Directories.OpenNavigation(ctx, "p", root)
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

func TestWholeTreePassPublishesOpenedTreesBeforeCollapsedOnes(t *testing.T) {
	c, root := indexFixture(t)
	c.SetScopes(testScopes{plane: sourcescope.Plane{DeferIgnored: true}})
	writeIndexFile(t, root.Path, ".gitignore", "output/\n")
	for _, dir := range []string{"src", "src/deep", "output", "output/nested"} {
		writeIndexFile(t, root.Path, dir+"/file.txt", "content")
	}
	dirs := []string{".", "src", "src/deep", "output", "output/nested"}
	var states laneStates
	unsubscribe := c.Directories.SubscribeNavigation(root, func() { states.record(t.Context(), c, root, dirs) })
	defer unsubscribe()
	testutil.FailErr(t, "start inventory", c.Directories.WarmNavigation(t.Context(), "p", root))
	testutil.FailErr(t, "await inventory", c.Directories.AwaitNavigation(t.Context(), "p", root))
	states.mu.Lock()
	defer states.mu.Unlock()
	for _, err := range states.errs {
		testutil.FailErr(t, "read published state", err)
	}
	if len(states.seen) < 2 {
		t.Fatalf("publications = %d, want the source lane and then the whole tree", len(states.seen))
	}
	first, last := states.seen[0], states.seen[len(states.seen)-1]
	if !slices.Equal(first, []bool{true, true, true, false, false}) {
		t.Fatalf("first publication completeness for %v = %v", dirs, first)
	}
	if !slices.Equal(last, []bool{true, true, true, true, true}) {
		t.Fatalf("final publication completeness for %v = %v", dirs, last)
	}
}

func TestSubtreeDemandSettlesOnTheBoundaryGeneration(t *testing.T) {
	c, root := indexFixture(t)
	c.SetScopes(testScopes{plane: sourcescope.Plane{DeferIgnored: true}})
	writeIndexFile(t, root.Path, ".gitignore", "output/\n")
	writeIndexFile(t, root.Path, "src/deep/file.txt", "content")
	writeIndexFile(t, root.Path, "output/nested/file.txt", "content")
	testutil.FailErr(t, "start inventory", c.Directories.WarmNavigation(t.Context(), "p", root))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	settled := func(ctx context.Context, navigation *Navigation) (bool, error) {
		return SubtreeCovered(ctx, navigation, "src")
	}
	testutil.FailErr(t, "await source subtree", c.Directories.AwaitSubtree(ctx, "p", root, "src", settled))
	navigation, err := c.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open navigation after subtree demand", err)
	defer func() { _ = navigation.Close() }()
	state, err := navigation.State(t.Context(), "src/deep")
	testutil.FailErr(t, "read source subtree state", err)
	if !state.Complete {
		t.Fatal("subtree demand settled without complete source coverage")
	}
	testutil.FailErr(t, "await inventory", c.Directories.AwaitNavigation(t.Context(), "p", root))
}

func TestCollapseBoundariesStopAtCollapsedTrees(t *testing.T) {
	c, root := indexFixture(t)
	c.SetScopes(testScopes{plane: sourcescope.Plane{DeferIgnored: true}})
	writeIndexFile(t, root.Path, ".gitignore", "build/\n")
	for _, dir := range []string{"src", "src/deep", "src/deep/build", "build", "build/nested", "build/nested/src"} {
		writeIndexFile(t, root.Path, dir+"/file.txt", "content")
	}
	// Boundaries never wait: they answer once the opened trees are published.
	boundaries := func(dir string) []string {
		t.Helper()
		var found []string
		testutil.WaitFor(t, 20*time.Second, func() bool {
			got, ready, err := c.Directories.CollapseBoundaries(t.Context(), "p", root, dir)
			testutil.FailErr(t, "list collapse boundaries", err)
			found = got
			return ready
		})
		return found
	}
	if got := boundaries("."); !slices.Equal(got, []string{"build", "src/deep/build"}) {
		t.Fatalf("boundaries under the root = %v", got)
	}
	if got := boundaries("src"); !slices.Equal(got, []string{"src/deep/build"}) {
		t.Fatalf("boundaries under src = %v", got)
	}
	// Everything under a collapsed directory is collapsed, so none begins there.
	if got := boundaries("build"); len(got) != 0 {
		t.Fatalf("boundaries under a collapsed tree = %v", got)
	}
}
