package repochange

import "testing"

func TestWatchObservationTracksBindingCoverageAndInterruptions(t *testing.T) {
	root := t.TempDir()
	r := NewWatcherRegistry()
	w := &worktreeWatcher{root: root, watched: map[string]watchRegistration{root: newWatchRegistration(1)}, done: make(chan struct{})}
	r.byDir[root] = w
	first := r.observeWatch(root, ".")
	if !first.Valid() || r.observeWatch(root, "child").Valid() {
		t.Fatal("directory registration was not respected")
	}
	delete(w.watched, root)
	if r.observeWatch(root, ".").Valid() {
		t.Fatal("revoked registration supplied a proof")
	}
	w.watched[root] = newWatchRegistration(1)
	if current := r.observeWatch(root, "."); !current.Valid() || current == first {
		t.Fatal("re-added directory revived the old proof")
	}
	first = r.observeWatch(root, ".")
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
	r.byDir[root] = &worktreeWatcher{root: root, watched: map[string]watchRegistration{root: newWatchRegistration(1)}}
	if current := r.observeWatch(root, "."); !current.Valid() || current == first {
		t.Fatal("replacement binding revived an old proof")
	}
	delete(r.byDir, root)
	if r.observeWatch(root, ".").Valid() {
		t.Fatal("closed binding supplied a proof")
	}
}

func TestRecursiveWatchObservationUsesRootRegistration(t *testing.T) {
	root := t.TempDir()
	r := NewWatcherRegistry()
	w := &worktreeWatcher{root: root, recursive: true, watched: map[string]watchRegistration{root: newWatchRegistration(0)}}
	r.byDir[root] = w
	first := r.observeWatch(root, "a/b")
	if !first.Valid() || first != r.observeWatch(root+"/.", "a/../a/b") {
		t.Fatal("canonical aliases changed the root observation")
	}
	delete(w.watched, root)
	if r.observeWatch(root, "a/b").Valid() {
		t.Fatal("unbound recursive root supplied a proof")
	}
	w.watched[root] = newWatchRegistration(0)
	if current := r.observeWatch(root, "a/b"); !current.Valid() || current == first {
		t.Fatal("recursive rebind revived a proof")
	}
}

func TestWatchScopeChangesWithoutInvalidatingUnrelatedDirectoryProofs(t *testing.T) {
	root := t.TempDir()
	r := NewWatcherRegistry()
	w := &worktreeWatcher{root: root, watched: map[string]watchRegistration{}}
	w.registerDirectoryLocked(root, 1)
	w.registerDirectoryLocked(root+"/src", 1)
	r.byDir[root] = w
	rootProof := r.observeWatch(root, ".")
	childProof := r.observeWatch(root, "src")
	scope := r.observeWatchScope(root)
	w.removeDirectoryLocked(root + "/src")
	w.registerDirectoryLocked(root+"/src", 1)
	if r.observeWatch(root, ".") != rootProof {
		t.Fatal("unrelated directory proof was invalidated")
	}
	if r.observeWatch(root, "src") == childProof || r.observeWatchScope(root) == scope {
		t.Fatal("child rebind did not advance preparation freshness")
	}
}
