package sourcesnapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestSnapshotPublishesAManifestWithoutCopyingBytes(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "src/main.go", "package main\n")

	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish snapshot", err)
	if snapshot.ID == "" || snapshot.ID != snapshot.MerkleSHA256 || snapshot.FileCount != 1 || snapshot.TotalBytes != 13 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Quality != CaptureExact {
		t.Fatalf("quiet tree published as %q, want %q", snapshot.Quality, CaptureExact)
	}
	if got := countRows(t, store, `SELECT count(*) FROM source_blob_objects`); got != 0 {
		t.Fatalf("a manifest stored %d blob objects; it names content, it does not copy it", got)
	}
	entry := lookupEntry(t, store, snapshot, "src/main.go")
	oids := sourceblob.ContentGitOIDs([]byte("package main\n"))
	if entry.Identity != IdentityHashed || entry.GitOID != oids.SHA1 || entry.SHA256 != sourceblob.ContentSHA([]byte("package main\n")) {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.ContentID() != "git:"+oids.SHA1 {
		t.Fatalf("content id = %q", entry.ContentID())
	}
	raw, err := store.Bytes(t.Context(), entry)
	testutil.FailErr(t, "read bytes from the live file", err)
	if string(raw) != "package main\n" {
		t.Fatalf("bytes = %q", raw)
	}
}

func TestSnapshotValidationQualityUpgrades(t *testing.T) {
	store := openSnapshotStore(t)
	request := Request{Roots: []Root{{Path: t.TempDir()}}}

	observed, err := store.publish(t.Context(), request, nil, nil, CaptureObserved, AdmissionScope)
	testutil.FailErr(t, "publish observed snapshot", err)
	_, err = store.publish(t.Context(), request, nil, nil, CaptureExact, AdmissionScope)
	testutil.FailErr(t, "publish exact snapshot", err)
	_, err = store.publish(t.Context(), request, nil, nil, CaptureObserved, AdmissionScope)
	testutil.FailErr(t, "republish observed snapshot", err)

	loaded, err := store.Get(t.Context(), observed.ID)
	testutil.FailErr(t, "load upgraded snapshot", err)
	if loaded.Quality != CaptureExact {
		t.Fatalf("quality = %q, want %q", loaded.Quality, CaptureExact)
	}
}

func TestSnapshotChangePublishesNewHeadWithoutMutatingOldManifest(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	request := Request{Roots: []Root{{Path: root}}}
	writeSource(t, root, "main.go", "first\n")
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first snapshot", err)

	writeSource(t, root, "main.go", "second\n")
	notifyChange(root, "main.go")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second snapshot", err)
	if first.ID == second.ID {
		t.Fatal("changed source reused the previous snapshot id")
	}
	before := lookupEntry(t, store, first, "main.go")
	after := lookupEntry(t, store, second, "main.go")
	if before.SHA256 != sourceblob.ContentSHA([]byte("first\n")) || after.SHA256 != sourceblob.ContentSHA([]byte("second\n")) {
		t.Fatalf("entries before=%+v after=%+v", before, after)
	}
	// Nothing retained the first version: not the ledger, not git, and the
	// live file has moved on. The manifest still names it exactly.
	if _, err := store.Bytes(t.Context(), before); !errors.Is(err, ErrContentUnavailable) {
		t.Fatalf("bytes of an unretained version: err = %v, want ErrContentUnavailable", err)
	}
	raw, err := store.Bytes(t.Context(), after)
	testutil.FailErr(t, "read current version", err)
	if string(raw) != "second\n" {
		t.Fatalf("current bytes = %q", raw)
	}
}

// A clean tracked file is identified by git's index and never read; git's
// object store then answers for its bytes after the worktree moves on.
func TestGitIndexIdentifiesCleanTrackedFilesWithoutReading(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "tracked.go", "package tracked\n")
	writeSource(t, root, "dirty.go", "package dirty\n")
	gittest.InitCommit(t, root, "base")
	writeSource(t, root, "dirty.go", "package dirty // edited\n")
	writeSource(t, root, "untracked.go", "package untracked\n")

	captured := captureCount(t, store)
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish", err)
	if got := captured(); got != 2 {
		t.Fatalf("files read = %d, want 2 (dirty.go and untracked.go); the index vouched for tracked.go", got)
	}
	tracked := lookupEntry(t, store, snapshot, "tracked.go")
	if tracked.Identity != IdentityIndex || tracked.SHA256 != "" || tracked.GitOID != sourceblob.ContentGitOIDs([]byte("package tracked\n")).SHA1 {
		t.Fatalf("tracked entry = %+v", tracked)
	}
	dirty := lookupEntry(t, store, snapshot, "dirty.go")
	if dirty.Identity != IdentityHashed || dirty.SHA256 == "" {
		t.Fatalf("dirty entry = %+v", dirty)
	}
	if lookupEntry(t, store, snapshot, "untracked.go").Identity != IdentityHashed {
		t.Fatal("untracked file was not read")
	}

	writeSource(t, root, "tracked.go", "package tracked // moved on\n")
	raw, err := store.Bytes(t.Context(), tracked)
	testutil.FailErr(t, "read the committed version from git", err)
	if string(raw) != "package tracked\n" {
		t.Fatalf("git bytes = %q", raw)
	}
	digest, err := store.Digest(t.Context(), dirty)
	testutil.FailErr(t, "digest a hashed entry", err)
	if digest != dirty.SHA256 {
		t.Fatalf("digest = %q want %q", digest, dirty.SHA256)
	}
}

// The same bytes have one content id however they were identified, so a
// commit that moves a file from hashed to index does not move the manifest.
func TestCommittingAFileKeepsTheGenerationIdentity(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	gittest.Init(t, root)
	writeSource(t, root, "a.go", "package a\n")
	request := Request{Roots: []Root{{Path: root}}}
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish untracked", err)
	if lookupEntry(t, store, first, "a.go").Identity != IdentityHashed {
		t.Fatal("untracked file was not hashed")
	}
	gittest.CommitAll(t, root, "add a")
	store.DiscardObservations(t.Context(), root)
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish tracked", err)
	if second.ID != first.ID {
		t.Fatalf("committing unchanged bytes moved the generation: %s vs %s", first.ID, second.ID)
	}
}

func TestSnapshotManifestsShareUnchangedHashBuckets(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	// Content verification surveys every file, so the change is seen without
	// waiting on a watcher event.
	request := Request{Roots: []Root{{Path: root}}, Verify: VerifyContent}
	for i := range 512 {
		writeSource(t, root, fmt.Sprintf("src/file-%03d.go", i), fmt.Sprintf("package src // %d\n", i))
	}
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first snapshot", err)

	writeSource(t, root, "src/file-255.go", "package src // changed\n")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second snapshot", err)
	if first.ID == second.ID {
		t.Fatal("changed source reused the previous snapshot id")
	}

	var firstChunks, secondChunks, sharedChunks, storedChunks int
	testutil.FailErr(t, "count first chunks", store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM source_snapshot_chunks WHERE snapshot_id = ?`, first.ID).Scan(&firstChunks))
	testutil.FailErr(t, "count second chunks", store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM source_snapshot_chunks WHERE snapshot_id = ?`, second.ID).Scan(&secondChunks))
	testutil.FailErr(t, "count shared chunks", store.db.QueryRowContext(t.Context(), `
		SELECT count(*) FROM source_snapshot_chunks a
		JOIN source_snapshot_chunks b ON b.chunk_id = a.chunk_id
		WHERE a.snapshot_id = ? AND b.snapshot_id = ?`, first.ID, second.ID).Scan(&sharedChunks))
	testutil.FailErr(t, "count stored chunks", store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM source_manifest_chunks`).Scan(&storedChunks))
	if firstChunks < 100 || secondChunks != firstChunks {
		t.Fatalf("bucket coverage first=%d second=%d", firstChunks, secondChunks)
	}
	if sharedChunks != firstChunks-1 {
		t.Fatalf("shared chunks = %d, want %d", sharedChunks, firstChunks-1)
	}
	if storedChunks != firstChunks+1 {
		t.Fatalf("stored chunks = %d, want %d", storedChunks, firstChunks+1)
	}
	if second.FileCount != 512 || second.TotalBytes <= 0 {
		t.Fatalf("second counts = %d files %d bytes", second.FileCount, second.TotalBytes)
	}
}

// A delta over a covered base reads only the buckets it touches and reuses
// the rest by chunk id.
func TestDeltaPublishReusesUntouchedChunksWithoutReadingThem(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	request := Request{Roots: []Root{{Path: root}}}
	for i := range 512 {
		writeSource(t, root, fmt.Sprintf("src/file-%03d.go", i), fmt.Sprintf("package src // %d\n", i))
	}
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first", err)
	writeSource(t, root, "src/file-007.go", "package src // changed\n")
	notifyChange(root, "src/file-007.go")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish delta", err)
	var stored int
	testutil.FailErr(t, "count stored chunks", store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM source_manifest_chunks`).Scan(&stored))
	var firstChunks int
	testutil.FailErr(t, "count first chunks", store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM source_snapshot_chunks WHERE snapshot_id = ?`, first.ID).Scan(&firstChunks))
	if stored != firstChunks+1 {
		t.Fatalf("stored chunks = %d want %d: a one-file delta rewrites one bucket", stored, firstChunks+1)
	}
	if second.FileCount != 512 {
		t.Fatalf("delta file count = %d", second.FileCount)
	}
	changes := 0
	testutil.FailErr(t, "diff", store.Diff(t.Context(), first.ID, second.ID, func(change Change) error {
		changes++
		if change.Path() != "src/file-007.go" || change.Before == nil || change.After == nil {
			t.Fatalf("unexpected change %+v", change)
		}
		return nil
	}))
	if changes != 1 {
		t.Fatalf("changes = %d want 1", changes)
	}
}

func TestDiffNamesAddedChangedAndRemovedFiles(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	request := Request{Roots: []Root{{Path: root}}}
	writeSource(t, root, "same.go", "same\n")
	writeSource(t, root, "changed.go", "before\n")
	writeSource(t, root, "gone.go", "gone\n")
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first", err)
	writeSource(t, root, "changed.go", "after\n")
	writeSource(t, root, "added.go", "added\n")
	testutil.FailErr(t, "remove", os.Remove(filepath.Join(root, "gone.go")))
	notifyChange(root, "changed.go", "added.go", "gone.go")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second", err)

	got := map[string]string{}
	testutil.FailErr(t, "diff", store.Diff(t.Context(), first.ID, second.ID, func(change Change) error {
		switch {
		case change.Before == nil:
			got[change.Path()] = "added"
		case change.After == nil:
			got[change.Path()] = "removed"
		default:
			got[change.Path()] = "changed"
		}
		return nil
	}))
	want := map[string]string{"added.go": "added", "changed.go": "changed", "gone.go": "removed"}
	if len(got) != len(want) {
		t.Fatalf("diff = %v want %v", got, want)
	}
	for path, kind := range want {
		if got[path] != kind {
			t.Fatalf("diff = %v want %v", got, want)
		}
	}
	all := 0
	testutil.FailErr(t, "diff from nothing", store.Diff(t.Context(), "", second.ID, func(change Change) error {
		if change.Before != nil {
			t.Fatalf("diff from nothing reported a before: %+v", change)
		}
		all++
		return nil
	}))
	if all != 3 {
		t.Fatalf("entries from nothing = %d want 3", all)
	}
}

func TestEntriesUnderAndHasPathAnswerFromTheManifest(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "pkg/a.go", "a\n")
	writeSource(t, root, "pkg/deep/b.go", "b\n")
	writeSource(t, root, "pkgx/c.go", "c\n")
	writeSource(t, root, "top.go", "t\n")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish", err)

	under, err := store.EntriesUnder(t.Context(), snapshot.ID, root, "pkg")
	testutil.FailErr(t, "entries under pkg", err)
	if len(under) != 2 || under[0].Path != "pkg/a.go" || under[1].Path != "pkg/deep/b.go" {
		t.Fatalf("entries under pkg = %+v", under)
	}
	all, err := store.EntriesUnder(t.Context(), snapshot.ID, root, ".")
	testutil.FailErr(t, "entries under root", err)
	if len(all) != 4 {
		t.Fatalf("entries under root = %d", len(all))
	}
	for rel, want := range map[string]bool{"pkg": true, "pkg/deep": true, "pkg/a.go": true, "pkgx": true, "pk": false, "missing.go": false} {
		held, err := store.HasPath(t.Context(), snapshot.ID, root, rel)
		testutil.FailErr(t, "has path "+rel, err)
		if held != want {
			t.Fatalf("HasPath(%s) = %v want %v", rel, held, want)
		}
	}
}

func TestMovedSinceNamesFilesWhoseMetadataMoved(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "a.go", "a\n")
	writeSource(t, root, "b.go", "b\n")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish", err)
	moved, err := store.MovedSince(t.Context(), snapshot.ID, nil)
	testutil.FailErr(t, "moved since (quiet)", err)
	if len(moved) != 0 {
		t.Fatalf("moved = %v on a quiet tree", moved)
	}
	writeSource(t, root, "b.go", "b changed\n")
	testutil.FailErr(t, "remove a", os.Remove(filepath.Join(root, "a.go")))
	moved, err = store.MovedSince(t.Context(), snapshot.ID, nil)
	testutil.FailErr(t, "moved since (all)", err)
	if len(moved) != 2 {
		t.Fatalf("moved = %v want a.go and b.go", moved)
	}
	moved, err = store.MovedSince(t.Context(), snapshot.ID, []string{"b.go"})
	testutil.FailErr(t, "moved since (named)", err)
	if len(moved) != 1 || moved[0] != "b.go" {
		t.Fatalf("moved = %v want b.go", moved)
	}
}

// Consumers of one worktree share a publication.
func TestConcurrentConsumersOfOneWorktreeShareOnePublication(t *testing.T) {
	inventory, scan := openSharedSnapshotStores(t)
	root := t.TempDir()
	writeSource(t, root, "main.go", "content\n")
	request := Request{Roots: []Root{{Path: root}}}

	ids := make(chan string, 16)
	errs := make(chan error, 16)
	var group sync.WaitGroup
	for i := range 16 {
		group.Add(1)
		go func(store *Store) {
			defer group.Done()
			snapshot, err := store.Ensure(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			ids <- snapshot.ID
		}([]*Store{inventory, scan}[i%2])
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent snapshot: %v", err)
	}
	close(ids)
	seen := map[string]struct{}{}
	for id := range ids {
		seen[id] = struct{}{}
	}
	if len(seen) != 1 {
		t.Fatalf("one worktree published %d distinct identities: %v", len(seen), seen)
	}
}

// A noisy change counter does not prevent convergence.
func TestPublishConvergesWhileTheEpochMovesContinuously(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		writeSource(t, root, name, "package a // "+name+"\n")
	}
	repochange.EnsureRoot(t.Context(), root)

	stop := make(chan struct{})
	var noise sync.WaitGroup
	noise.Add(1)
	go func() {
		defer noise.Done()
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				repochange.Advance(root)
			}
		}
	}()
	defer func() {
		close(stop)
		noise.Wait()
	}()

	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish under epoch noise", err)
	if snapshot.FileCount != 3 {
		t.Fatalf("file count = %d, want 3", snapshot.FileCount)
	}
	if snapshot.Quality != CaptureExact {
		t.Fatalf("quality = %q; a moving epoch alone must not weaken a settled capture", snapshot.Quality)
	}
}

// Moving trees publish with observed quality.
func TestPublishLabelsAnUnsettledTreeRatherThanFailing(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	for i := range 8 {
		writeSource(t, root, filepath.Join("pkg", "f"+string(rune('a'+i))+".go"), "package pkg\n")
	}

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			writeSource(t, root, "churn.go", "package pkg // "+strconv.Itoa(n)+"\n")
			time.Sleep(time.Millisecond)
		}
	}()

	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	close(stop)
	writer.Wait()
	testutil.FailErr(t, "publish a continuously written tree", err)
	if snapshot.ID == "" || snapshot.FileCount < 8 {
		t.Fatalf("snapshot = %+v, want a published manifest of the settled files", snapshot)
	}
}

// Observations persist independently of manifests.
func TestObservationsSurviveAPublishThatNeverCommitted(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		writeSource(t, root, name, "package a // "+name+"\n")
	}
	request := Request{Roots: []Root{{Path: root}}}

	if _, ok, err := store.observations.Get(t.Context(), root, "a.go"); err != nil || ok {
		t.Fatalf("cold cache holds a.go: ok=%v err=%v", ok, err)
	}
	// One survey, not published.
	candidate, err := store.survey(t.Context(), request, request.Roots[0])
	testutil.FailErr(t, "run one survey", err)
	candidate.close(t.Context(), store)

	if published := countRows(t, store, `SELECT count(*) FROM source_snapshots`); published != 0 {
		t.Fatalf("a capture pass published %d snapshots on its own", published)
	}
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if _, ok, err := store.observations.Get(t.Context(), root, name); err != nil || !ok {
			t.Fatalf("durable observation for %s after an unpublished pass: ok=%v err=%v", name, ok, err)
		}
	}

	captured := captureCount(t, store)
	snapshot, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish with a warm baseline", err)
	if snapshot.FileCount != 3 {
		t.Fatalf("file count = %d, want 3", snapshot.FileCount)
	}
	if got := captured(); got != 0 {
		t.Fatalf("files re-read after an unpublished pass = %d, want 0", got)
	}
}

func TestIsCurrentIgnoresWritesOutsideTheAdmittedSet(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "main.go", "package main\n")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish snapshot", err)

	writeSource(t, root, "node_modules/dep/index.js", "module.exports = 2\n")
	repochange.Advance(root)
	current, err := store.IsCurrent(t.Context(), snapshot.ID)
	testutil.FailErr(t, "check currency", err)
	if !current {
		t.Fatal("a write to an excluded path reported the snapshot stale")
	}

	writeSource(t, root, "main.go", "package main // edited\n")
	current, err = store.IsCurrent(t.Context(), snapshot.ID)
	testutil.FailErr(t, "check currency after an admitted write", err)
	if current {
		t.Fatal("a write to an admitted file left the snapshot current")
	}
}

func TestIsCurrentRefreshesMetadataObservations(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	writeSource(t, root, "main.go", "package main\n")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish snapshot", err)
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat source", err)
	modified := info.ModTime().Add(time.Hour)
	testutil.FailErr(t, "touch source", os.Chtimes(path, modified, modified))
	// The watcher reports the touch, so the tracker cannot vouch for the
	// generation and IsCurrent verifies by reading.
	notifyChange(root, "main.go")

	current, err := store.IsCurrent(t.Context(), snapshot.ID)
	testutil.FailErr(t, "check touched snapshot", err)
	if !current {
		t.Fatal("metadata-only change reported stale content")
	}
	observation, ok, err := store.observations.Get(t.Context(), root, "main.go")
	testutil.FailErr(t, "load refreshed observation", err)
	if !ok || observation.ModifiedNS != modified.UnixNano() {
		t.Fatalf("metadata observation was not refreshed: %+v", observation)
	}
}

// A complete survey forgets the cache rows of paths that left the tree, so
// a churning tree cannot grow the cache without bound.
func TestSurveyForgetsVanishedPaths(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "a.go", "package a\n")
	writeSource(t, root, "gone/b.go", "package b\n")
	request := Request{Roots: []Root{{Path: root}}}
	_, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first", err)
	if _, ok, err := store.observations.Get(t.Context(), root, "gone/b.go"); err != nil || !ok {
		t.Fatalf("cache row for gone/b.go: ok=%v err=%v", ok, err)
	}
	testutil.FailErr(t, "remove", os.RemoveAll(filepath.Join(root, "gone")))
	candidate, err := store.survey(t.Context(), request, request.Roots[0])
	testutil.FailErr(t, "survey again", err)
	candidate.close(t.Context(), store)
	if _, ok, err := store.observations.Get(t.Context(), root, "gone/b.go"); err != nil || ok {
		t.Fatalf("cache row for a vanished path survived the survey: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.observations.Get(t.Context(), root, "a.go"); err != nil || !ok {
		t.Fatalf("cache row for a live path was forgotten: ok=%v err=%v", ok, err)
	}
}

// A delta that drops a path forgets its cache row too.
func TestDeltaForgetsDroppedPaths(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "a.go", "package a\n")
	writeSource(t, root, "gone/b.go", "package b\n")
	request := Request{Roots: []Root{{Path: root}}}
	_, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first", err)
	testutil.FailErr(t, "remove", os.RemoveAll(filepath.Join(root, "gone")))
	notifyChange(root, "gone")
	surveys := surveyCount(t, store)
	_, err = store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish delta", err)
	if surveys() != 0 {
		t.Fatal("the removal was not applied as a delta")
	}
	if _, ok, err := store.observations.Get(t.Context(), root, "gone/b.go"); err != nil || ok {
		t.Fatalf("cache row for a dropped path survived the delta: ok=%v err=%v", ok, err)
	}
}

func TestForgetRootDropsOnlyDetachedRootObservations(t *testing.T) {
	store := openSnapshotStore(t)
	kept, gone := t.TempDir(), t.TempDir()
	writeSource(t, kept, "a.go", "package a\n")
	writeSource(t, gone, "b.go", "package b\n")
	for _, root := range []string{kept, gone} {
		_, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
		testutil.FailErr(t, "publish "+root, err)
	}
	testutil.FailErr(t, "forget detached root", store.DiscardObservations(t.Context(), gone))

	if _, ok, err := store.observations.Get(t.Context(), kept, "a.go"); err != nil || !ok {
		t.Fatalf("live root observation was removed: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.observations.Get(t.Context(), gone, "b.go"); err != nil || ok {
		t.Fatalf("detached root observation survived: ok=%v err=%v", ok, err)
	}
}

func TestSweepRemovesOnlyUnreachableSnapshotManifests(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	request := Request{Roots: []Root{{Path: root}}}
	writeSource(t, root, "main.go", "first\n")
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first snapshot", err)

	writeSource(t, root, "main.go", "second\n")
	notifyChange(root, "main.go")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second snapshot", err)
	if second.ID == first.ID {
		t.Fatal("the delivered change did not publish a successor")
	}
	old := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	_, err = store.db.ExecContext(t.Context(), `UPDATE source_snapshots SET created_ts = ? WHERE id = ?`, old, first.ID)
	testutil.FailErr(t, "age first snapshot", err)
	testutil.FailErr(t, "sweep source snapshots", store.Sweep(t.Context(), time.Now().UTC().Add(-time.Hour)))

	_, err = store.Get(t.Context(), first.ID)
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("expired snapshot error = %v want ErrSnapshotNotFound", err)
	}
	current, err := store.Get(t.Context(), second.ID)
	testutil.FailErr(t, "load current snapshot", err)
	if current.ID != second.ID {
		t.Fatalf("current snapshot = %q want %q", current.ID, second.ID)
	}
	if _, ok, err := store.Lookup(t.Context(), second.ID, root, "main.go"); err != nil || !ok {
		t.Fatalf("current manifest lost its entry: ok=%v err=%v", ok, err)
	}
}

func TestSweepExpiresUnusedRootsHead(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "main.go", "package main\n")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish snapshot", err)
	testutil.FailErr(t, "expire snapshot head", store.Sweep(t.Context(), time.Now().UTC().Add(time.Hour)))
	_, err = store.Get(t.Context(), snapshot.ID)
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("expired snapshot error = %v want ErrSnapshotNotFound", err)
	}
}

func TestSweepKeepsCurrentProjectInventorySnapshot(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "main.go", "package main\n")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish snapshot", err)
	testdbseed.InsertProject(t, store.db, "project-inventory")
	_, err = store.db.ExecContext(t.Context(), `
		INSERT INTO source_inventory_state (
			project_id, requested_generation, completed_generation, phase,
			file_count, requested_ts, snapshot_id
		) VALUES (?, 1, 1, 'ready', 1, ?, ?)
	`, "project-inventory", time.Now().UTC().Format(time.RFC3339Nano), snapshot.ID)
	testutil.FailErr(t, "claim inventory snapshot", err)
	testutil.FailErr(t, "expire snapshot head", store.Sweep(t.Context(), time.Now().UTC().Add(time.Hour)))
	current, err := store.Get(t.Context(), snapshot.ID)
	testutil.FailErr(t, "load inventory snapshot", err)
	if current.ID != snapshot.ID {
		t.Fatalf("current snapshot = %q want %q", current.ID, snapshot.ID)
	}
}

// A file the host cannot hash is identified by its stat facts and still
// published; its bytes come from the live file while it holds still.
func TestOversizedFilesAreIdentifiedByStat(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "small.go", "package small\n")
	big := filepath.Join(root, "big.bin")
	file, err := os.Create(big)
	testutil.FailErr(t, "create big file", err)
	testutil.FailErr(t, "size big file", file.Truncate(maxHashBytes+1))
	testutil.FailErr(t, "close big file", file.Close())

	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish", err)
	entry := lookupEntry(t, store, snapshot, "big.bin")
	if entry.Identity != IdentityStat || entry.ContentID() != "" || entry.Size != maxHashBytes+1 {
		t.Fatalf("big entry = %+v", entry)
	}
	if !entry.SameContent(entry) || entry.SameContent(lookupEntry(t, store, snapshot, "small.go")) {
		t.Fatal("stat identity compares by its stat facts alone")
	}
	moved, err := store.MovedSince(t.Context(), snapshot.ID, []string{"big.bin"})
	testutil.FailErr(t, "moved since", err)
	if len(moved) != 0 {
		t.Fatalf("moved = %v", moved)
	}
}
