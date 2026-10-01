package sourcesnapshot

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func openSnapshotStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	broker := backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
		backgroundwork.ResourceMetadata: {Total: 1},
		backgroundwork.ResourceIO:       {Total: 1},
		backgroundwork.ResourceCPU:      {Total: 1},
	})
	blobs := sourceblob.New(filepath.Join(dir, "content"))
	store := New(database, blobs, filepath.Join(dir, "observations.db"), broker)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func openSharedSnapshotStores(t *testing.T) (*Store, *Store) {
	t.Helper()
	dir := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	blobs := sourceblob.New(filepath.Join(dir, "content"))
	build := func() *Store {
		broker := backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
			backgroundwork.ResourceIO: {Total: 1},
		})
		return New(database, blobs, filepath.Join(dir, "observations.db"), broker)
	}
	return build(), build()
}

func writeSource(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir source parent", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write source", os.WriteFile(path, []byte(content), 0o644))
}

func captureCount(t *testing.T, store *Store) func() int {
	t.Helper()
	var mu sync.Mutex
	seen := 0
	store.onCapture = func(string) {
		mu.Lock()
		seen++
		mu.Unlock()
	}
	t.Cleanup(func() { store.onCapture = nil })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

func surveyCount(t *testing.T, store *Store) func() int {
	t.Helper()
	var mu sync.Mutex
	seen := 0
	store.onSurvey = func(string) {
		mu.Lock()
		seen++
		mu.Unlock()
	}
	t.Cleanup(func() { store.onSurvey = nil })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

func admittedPaths(t *testing.T, root string, scope *sourcescope.Scope, relDir string) ([]fileRef, []Boundary) {
	t.Helper()
	var refs []fileRef
	boundaries, err := admittedUnder(t.Context(), Root{Path: root}, scope, relDir, func(ref fileRef) error {
		refs = append(refs, ref)
		return nil
	})
	testutil.FailErr(t, "survey "+relDir, err)
	return refs, boundaries
}

func refPaths(refs []fileRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Path)
	}
	return out
}

// Relative paths identify entries only within a single root.
func entryPaths(t *testing.T, store *Store, snapshot Snapshot) map[string]string {
	t.Helper()
	out := make(map[string]string)
	testutil.FailErr(t, "read snapshot entries", store.ForEachEntry(t.Context(), snapshot.ID, func(entry Entry) error {
		out[entry.Path] = entry.ContentID()
		return nil
	}))
	return out
}

// lookupEntry returns one admitted file of a single-root snapshot.
func lookupEntry(t *testing.T, store *Store, snapshot Snapshot, rel string) Entry {
	t.Helper()
	if len(snapshot.Roots) != 1 {
		t.Fatalf("snapshot roots = %v, want one", snapshot.Roots)
	}
	entry, ok, err := store.Lookup(t.Context(), snapshot.ID, snapshot.Roots[0].Path, rel)
	testutil.FailErr(t, "lookup "+rel, err)
	if !ok {
		t.Fatalf("snapshot %s does not hold %s", snapshot.ID, rel)
	}
	return entry
}
