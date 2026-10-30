package repochange

import "testing"

func TestWatchObservationTracksBindingCoverageAndInterruptions(t *testing.T) {
	root := t.TempDir()
	r := NewWatcherRegistry()
	w := &worktreeWatcher{root: root, watched: map[string]int{root: 1}, done: make(chan struct{})}
	r.byDir[root] = w
	first := r.observeWatch(root, ".")
	if !first.Valid() || r.observeWatch(root, "child").Valid() {
		t.Fatal("directory registration was not respected")
	}
	w.faulted = true
	if r.observeWatch(root, ".").Valid() {
		t.Fatal("faulted stream supplied a proof")
	}
	w.faulted = false
	w.overflowed = true
	if r.observeWatch(root, ".").Valid() {
		t.Fatal("overflow window supplied a proof")
	}
	w.overflowed = false
	w.overflows++
	if current := r.observeWatch(root, "."); !current.Valid() || current == first {
		t.Fatal("closed overflow revived an old proof")
	}
	r.byDir[root] = &worktreeWatcher{root: root, watched: map[string]int{root: 1}}
	if current := r.observeWatch(root, "."); !current.Valid() || current == first {
		t.Fatal("replacement binding revived an old proof")
	}
	delete(r.byDir, root)
	if r.observeWatch(root, ".").Valid() {
		t.Fatal("closed binding supplied a proof")
	}
}
