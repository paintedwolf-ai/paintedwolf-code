package sourcesnapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Synthetic coverage exercises delta capture without a platform watcher.
func coverRoot(t *testing.T, root string) {
	t.Helper()
	repochange.ResetWatchersForTest()
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(repochange.ResetWatchersForTest)
}

func notifyChange(root string, paths ...string) {
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		abs = append(abs, filepath.Join(root, filepath.FromSlash(p)))
	}
	repochange.Notify(context.Background(), repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: abs, Source: repochange.SourceWatcher,
	})
}

func TestSecondGenerationCostsOnlyWhatChanged(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	for _, name := range []string{"a.go", "b.go", "deep/c.go"} {
		writeSource(t, root, name, "package x // "+name+"\n")
	}
	request := Request{Roots: []Root{{Path: root}}}
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)
	if first.Quality != CaptureExact {
		t.Fatalf("first generation quality = %q", first.Quality)
	}

	captured := captureCount(t, store)
	surveys := surveyCount(t, store)
	writeSource(t, root, "b.go", "package x // changed\n")
	notifyChange(root, "b.go")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second generation", err)
	if second.ID == first.ID {
		t.Fatal("a content change must publish a new generation")
	}
	if got := captured(); got != 1 {
		t.Fatalf("files re-read = %d, want 1 (only b.go)", got)
	}
	if got := surveys(); got != 0 {
		t.Fatalf("root surveys = %d, want 0: the generation came from the delta", got)
	}
	paths := entryPaths(t, store, second)
	if len(paths) != 3 || paths["b.go"] == entryPaths(t, store, first)["b.go"] {
		t.Fatalf("second generation entries = %v", paths)
	}
}

func TestDeltaAddsNewDirectoriesAndDropsVanishedOnes(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "keep.go", "package x\n")
	writeSource(t, root, "old/gone.go", "package old\n")
	request := Request{Roots: []Root{{Path: root}}}
	_, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)

	testutil.FailErr(t, "remove old", os.RemoveAll(filepath.Join(root, "old")))
	writeSource(t, root, "new/sub/added.go", "package added\n")
	writeSource(t, root, "new/other.go", "package other\n")
	notifyChange(root, "old", "new")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second generation", err)
	paths := entryPaths(t, store, second)
	if _, gone := paths["old/gone.go"]; gone {
		t.Fatalf("vanished directory still present: %v", paths)
	}
	for _, want := range []string{"keep.go", "new/sub/added.go", "new/other.go"} {
		if _, ok := paths[want]; !ok {
			t.Fatalf("missing %s in %v", want, paths)
		}
	}
	if second.Quality != CaptureExact || second.FileCount != 3 {
		t.Fatalf("quality = %q files = %d, want exact with 3 files", second.Quality, second.FileCount)
	}
}

func TestIgnoreFileChangeForcesASurvey(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "src/a.go", "package a\n")
	writeSource(t, root, "gen/out.go", "package gen\n")
	request := Request{Roots: []Root{{Path: root}}}
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)
	if _, ok := entryPaths(t, store, first)["gen/out.go"]; !ok {
		t.Fatal("gen/out.go admitted before the ignore file exists")
	}

	surveys := surveyCount(t, store)
	writeSource(t, root, ".gitignore", "gen/\n")
	notifyChange(root, ".gitignore")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second generation", err)
	if got := surveys(); got != 1 {
		t.Fatalf("root surveys = %d, want 1: an ignore file change re-decides the whole root", got)
	}
	if _, ok := entryPaths(t, store, second)["gen/out.go"]; ok {
		t.Fatal("gen/out.go still admitted after the ignore file excluded it")
	}
}

func TestIsCurrentChecksDiskBeforeWatcherDelivery(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "a.go", "package a\n")
	request := Request{Roots: []Root{{Path: root}}}
	snapshot, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish", err)

	surveys := surveyCount(t, store)
	current, err := store.IsCurrent(t.Context(), snapshot.ID)
	testutil.FailErr(t, "IsCurrent", err)
	if !current || surveys() != 0 {
		t.Fatalf("current = %v surveys = %d; want true with no survey", current, surveys())
	}
	writeSource(t, root, "a.go", "package a // moved\n")
	current, err = store.IsCurrent(t.Context(), snapshot.ID)
	testutil.FailErr(t, "IsCurrent after change", err)
	if current {
		t.Fatal("a changed file must not report current")
	}
}

func TestChangesDuringACaptureArePickedUpBeforePublishing(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "a.go", "package a\n")
	request := Request{Roots: []Root{{Path: root}}}
	_, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first", err)

	// A watcher reports b.go changing while a.go is captured.
	writeSource(t, root, "a.go", "package a // v2\n")
	notifyChange(root, "a.go")
	store.onCapture = func(string) {
		store.onCapture = nil
		writeSource(t, root, "b.go", "package b\n")
		notifyChange(root, "b.go")
	}
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second", err)
	paths := entryPaths(t, store, second)
	if _, ok := paths["b.go"]; !ok {
		t.Fatalf("a change that arrived mid-capture was missed: %v", paths)
	}
	if second.Quality != CaptureExact {
		t.Fatalf("quality = %q, want exact once the tree settled", second.Quality)
	}
}

func TestIgnoredChurnDoesNotMoveTheGeneration(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, ".gitignore", "build/\n")
	writeSource(t, root, "src/a.go", "package a\n")
	writeSource(t, root, "build/out.o", "x")
	request := Request{Roots: []Root{{Path: root}}}
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)

	surveys := surveyCount(t, store)
	writeSource(t, root, "build/out.o", "xx")
	writeSource(t, root, "build/more.o", "y")
	notifyChange(root, "build/out.o", "build/more.o")
	current, err := store.IsCurrent(t.Context(), first.ID)
	testutil.FailErr(t, "check current", err)
	if !current {
		t.Fatal("churn under an ignored directory reported the generation stale")
	}
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish after ignored churn", err)
	if second.ID != first.ID || second.Quality != CaptureExact {
		t.Fatalf("ignored churn produced generation %s quality %q; want %s exact", second.ID, second.Quality, first.ID)
	}
	if got := surveys(); got != 0 {
		t.Fatalf("root surveys = %d, want 0", got)
	}
}

// Changes during publication patch the surveyed candidate.
func TestChangesAfterASurveyAreAppliedAsADelta(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "a.go", "package a\n")
	surveys := surveyCount(t, store)
	store.onSurvey = func(string) {
		store.onSurvey = nil
		surveys = surveyCount(t, store)
		writeSource(t, root, "late.go", "package late\n")
		notifyChange(root, "late.go")
	}
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish", err)
	if _, ok := entryPaths(t, store, snapshot)["late.go"]; !ok {
		t.Fatal("a file written during the survey was not admitted")
	}
	if got := surveys(); got != 0 {
		t.Fatalf("surveys after the first = %d, want 0: the late write was applied as a delta", got)
	}
}

func TestUnreadableBoundaryCannotPublishExactCoverage(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "readable.go", "package readable\n")
	req := Request{Roots: []Root{{Path: root}}, Verify: VerifyContent}
	candidate, err := store.candidate(t.Context(), req, req.Roots[0], deltaPlan{})
	testutil.FailErr(t, "prepare readable source", err)
	defer candidate.close(t.Context(), store)
	candidate.boundaries["unreadable"] = Boundary{RootPath: root, Path: "unreadable", Reason: "unreadable", Detail: "permission denied"}
	snapshot, err := store.publishCandidates(t.Context(), req, []rootCapture{{candidate: candidate}}, CaptureExact, time.Now())
	testutil.FailErr(t, "publish incomplete observation", err)
	if snapshot.Quality != CaptureObserved || snapshot.FileCount != 1 {
		t.Fatalf("incomplete source became exact or lost readable files: %+v", snapshot)
	}
	if gaps := snapshot.Unobserved(); len(gaps) != 1 || gaps[0].Budgeted() {
		t.Fatalf("I/O failure became a scope or size policy: %+v", gaps)
	}
}

func TestMultiRootCaptureKeepsDeltasIndependent(t *testing.T) {
	store := openSnapshotStore(t)
	roots := []Root{{Path: t.TempDir()}, {Path: t.TempDir()}}
	coverRoot(t, roots[0].Path)
	repochange.MarkCoverageCompleteForTest(roots[1].Path)
	for _, root := range roots {
		writeSource(t, root.Path, "main.go", "package initial\n")
	}
	request := Request{Roots: roots}
	previous, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish both roots", err)
	captured, surveys := captureCount(t, store), surveyCount(t, store)
	for _, changed := range roots {
		writeSource(t, changed.Path, "main.go", "package changed\n")
		notifyChange(changed.Path, "main.go")
		next, err := store.Ensure(t.Context(), request)
		testutil.FailErr(t, "publish root delta", err)
		if next.Quality != CaptureExact || next.FileCount != 2 {
			t.Fatalf("root delta quality = %q, files = %d; want exact with two files", next.Quality, next.FileCount)
		}
		for _, root := range roots {
			before, found, err := store.Lookup(t.Context(), previous.ID, root.Path, "main.go")
			testutil.FailErr(t, "read previous root entry", err)
			if !found {
				t.Fatalf("previous snapshot lost root %s", root.Path)
			}
			after, found, err := store.Lookup(t.Context(), next.ID, root.Path, "main.go")
			testutil.FailErr(t, "read current root entry", err)
			if !found || (before.ContentID() != after.ContentID()) != (root.Path == changed.Path) {
				t.Fatalf("root %s content changed incorrectly while updating %s", root.Path, changed.Path)
			}
		}
		previous = next
	}
	if got := captured(); got != 2 {
		t.Fatalf("files captured = %d, want two changed files", got)
	}
	if got := surveys(); got != 0 {
		t.Fatalf("root surveys = %d, want incremental capture only", got)
	}
}
