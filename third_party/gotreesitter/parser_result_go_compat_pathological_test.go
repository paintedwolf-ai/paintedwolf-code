package gotreesitter

import (
	"testing"
	"time"
)

// runWithin runs fn and fails if it does not return within d. A regression that
// reintroduces the un-deduped descent would loop forever on the cyclic trees
// below, so this converts a hang into a deterministic failure.
func runWithin(t *testing.T, d time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not terminate within %v (cyclic-descent guard regressed)", name, d)
	}
}

// buildCyclicGoTree builds source_file -> dot -> (back to source_file). A
// recovery-mode transient go tree can contain exactly this kind of back-edge;
// the normalizer must terminate on it rather than re-descend the cycle forever.
func buildCyclicGoTree() (*Node, *nodeArena) {
	arena := newNodeArena(arenaClassFull)
	dot := newLeafNodeInArena(arena, 2 /* dot */, true, 0, 1, Point{}, Point{Column: 1})
	root := newParentNodeInArena(arena, 3 /* source_file */, true, []*Node{dot}, nil, 0)
	dot.children = cloneNodeSliceInArena(arena, []*Node{root}) // back-edge -> cycle
	return root, arena
}

func TestNormalizeGoCompatibilitySubtreeTerminatesOnCycle(t *testing.T) {
	lang := goDotCompatibilityTestLanguage()
	syms, _ := goCompatibilitySymbolsForLanguage(lang)
	root, _ := buildCyclicGoTree()
	source := []byte(".")
	runWithin(t, 15*time.Second, "normalizeGoCompatibilitySubtreeWithStopAndScratch", func() {
		poller := parseStopPoller{}
		normalizeGoCompatibilitySubtreeWithStopAndScratch(root, source, syms, goCompatibilitySourceFlags{}, nil, &poller, nil)
	})
}
