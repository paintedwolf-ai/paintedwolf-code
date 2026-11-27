package repochange

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

func TestAdoptCreatedTreeRegistersNestedDirsAndReportsSurvivingChild(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	cfg, err := sourcescope.DefaultConfig()
	testutil.FailErr(t, "default scope config", err)

	nested := filepath.Join(root, "nested", "a", "b", "c")
	testutil.FailErr(t, "mkdir nested chain", os.MkdirAll(nested, 0o755))
	surviving := filepath.Join(nested, "surviving.txt")
	testutil.FailErr(t, "write surviving child", os.WriteFile(surviving, []byte("hello"), 0o644))

	// Include a symlinked directory to verify adoption never descends into symlinks.
	outside := t.TempDir()
	testutil.FailErr(t, "write outside file", os.WriteFile(filepath.Join(outside, "escaped.txt"), []byte("nope"), 0o644))
	testutil.FailErr(t, "symlink outside dir", os.Symlink(outside, filepath.Join(root, "nested", "linked")))

	platform := &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error), resync: make(chan struct{})}
	w := &worktreeWatcher{
		root:    root,
		watcher: platform,
		budget:  watchfd.NewBudget(64),
		maxDirs: 64,
		done:    make(chan struct{}),
		watched: map[string]int{root: 1},
		scope:   sourcescope.New(root, sourcescope.Options{Plane: cfg.Catalog}),
	}

	changes := w.adoptCreatedTree(filepath.Join(root, "nested"), "nested")
	wantPaths := []string{
		"nested",
		"nested/a",
		"nested/a/b",
		"nested/a/b/c",
		"nested/a/b/c/surviving.txt",
		"nested/linked",
	}
	gotPaths := make([]string, 0, len(changes))
	for _, ch := range changes {
		if ch.Kind == WorktreeChangeResync {
			t.Fatalf("unexpected resync under ample budget: %+v", changes)
		}
		gotPaths = append(gotPaths, ch.Path)
	}
	for _, want := range wantPaths {
		if !slices.Contains(gotPaths, want) {
			t.Fatalf("adopted changes = %v, missing %q", gotPaths, want)
		}
	}
	if slices.Contains(gotPaths, "nested/linked/escaped.txt") {
		t.Fatalf("adoption followed symlinked directory: %v", gotPaths)
	}
	for _, dir := range []string{"nested", "nested/a", "nested/a/b", "nested/a/b/c"} {
		if !w.watchesDir(filepath.Join(root, filepath.FromSlash(dir))) {
			t.Fatalf("adopted directory %q was not registered", dir)
		}
	}
	if w.watchesDir(filepath.Join(root, "nested", "linked")) {
		t.Fatal("symlinked directory was registered as a watched directory")
	}
}

func TestAdoptCreatedTreeEmitsResyncWhenBudgetExhausted(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	cfg, err := sourcescope.DefaultConfig()
	testutil.FailErr(t, "default scope config", err)

	nested := filepath.Join(root, "nested", "a", "b", "c")
	testutil.FailErr(t, "mkdir nested chain", os.MkdirAll(nested, 0o755))
	testutil.FailErr(t, "write surviving child", os.WriteFile(filepath.Join(nested, "surviving.txt"), []byte("hello"), 0o644))

	platform := &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error), resync: make(chan struct{})}
	w := &worktreeWatcher{
		root:    root,
		watcher: platform,
		budget:  watchfd.NewBudget(64),
		maxDirs: 2, // root + 1 dir only; cannot register nested/a/b/c
		done:    make(chan struct{}),
		watched: map[string]int{root: 1},
		scope:   sourcescope.New(root, sourcescope.Options{Plane: cfg.Catalog}),
	}

	changes := w.adoptCreatedTree(filepath.Join(root, "nested"), "nested")
	if len(changes) != 1 || changes[0].Path != "." || changes[0].Kind != WorktreeChangeResync {
		t.Fatalf("budget-exhausted adoption = %+v, want single bare resync", changes)
	}
	if cov := w.coverage(); cov.Truncated == 0 || cov.Complete {
		t.Fatalf("coverage after budget exhaustion = %+v, want Truncated>0 Complete=false", cov)
	}
}

func TestAdoptCreatedTreeBoundsInspectedEntriesInWideExcludedDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	cfg, err := sourcescope.DefaultConfig()
	testutil.FailErr(t, "default scope config", err)

	wide := filepath.Join(root, "wide")
	testutil.FailErr(t, "mkdir wide", os.MkdirAll(wide, 0o755))
	// Populate 2,000 entries inside a newly created directory, plus a symlink
	// pointing outside root to prove root confinement and bounded chunk reads.
	outside := t.TempDir()
	testutil.FailErr(t, "write outside sentinel", os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("x"), 0o644))
	testutil.FailErr(t, "symlink outside", os.Symlink(outside, filepath.Join(wide, "escape")))
	for i := range 2000 {
		// Include boundary-excluded .DS_Store / normal files so excluded entries
		// are also charged against the cap.
		name := fmt.Sprintf("f-%04d.txt", i)
		testutil.FailErr(t, "write wide file", os.WriteFile(filepath.Join(wide, name), nil, 0o644))
	}

	platform := &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error), resync: make(chan struct{})}
	w := &worktreeWatcher{
		root:    root,
		watcher: platform,
		budget:  watchfd.NewBudget(64),
		maxDirs: 64,
		done:    make(chan struct{}),
		watched: map[string]int{root: 1},
		scope:   sourcescope.New(root, sourcescope.Options{Plane: cfg.Catalog}),
	}

	changes, inspected := w.adoptCreatedTreeMeasured(wide, "wide")
	if len(changes) != 1 || changes[0].Path != "." || changes[0].Kind != WorktreeChangeResync {
		t.Fatalf("wide directory adoption = %+v, want single bare resync", changes)
	}
	// Chunked ReadDir must stop immediately upon crossing maxAdoptedChanges
	// rather than reading all 2,000 directory entries.
	if inspected != maxAdoptedChanges+1 {
		t.Fatalf("inspected entries = %d, want %d (bounded stop)", inspected, maxAdoptedChanges+1)
	}
	if w.watchesDir(filepath.Join(wide, "escape")) {
		t.Fatal("symlinked outside directory was registered")
	}
}
