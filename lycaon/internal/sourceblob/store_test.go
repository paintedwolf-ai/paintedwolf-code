package sourceblob

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func TestPutRejectsMismatchedHash(t *testing.T) {
	store := New(t.TempDir())
	if _, _, _, err := store.Put(ContentSHA([]byte("a")), []byte("b")); err == nil {
		t.Fatal("mismatched content hash accepted")
	}
}

func TestMaintenanceYieldsToReferencesWithoutQueueingBehindThem(t *testing.T) {
	store := New(t.TempDir())
	releaseReference := store.AcquireReferenceLease()
	if release, ok, err := store.TryAcquireMaintenanceLease(); ok || err != nil {
		if release != nil {
			release()
		}
		t.Fatal("maintenance entered during a reference lease")
	}
	// A refused try leaves nothing pending: the next capture starts at once.
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.AcquireReferenceLease()()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a second reference lease waited behind a refused maintenance try")
	}
	releaseReference()

	releaseMaintenance, ok, err := store.TryAcquireMaintenanceLease()
	testutil.FailErr(t, "acquire maintenance lease", err)
	if !ok {
		t.Fatal("maintenance refused with no reference lease held")
	}
	if store.lifecycle.TryRLock() {
		store.lifecycle.RUnlock()
		t.Fatal("a reference entered during maintenance")
	}
	releaseMaintenance()
}

func TestPutFileRoundTripsContentAndStat(t *testing.T) {
	store := New(t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	testutil.FailErr(t, "write source", os.WriteFile(path, []byte("package main\n"), 0o644))

	got, err := store.PutFile(t.Context(), path)
	testutil.FailErr(t, "capture file", err)
	if got.SHA256 != ContentSHA([]byte("package main\n")) {
		t.Fatalf("digest = %q", got.SHA256)
	}
	info, err := os.Lstat(path)
	testutil.FailErr(t, "stat source", err)
	if got.Size != info.Size() || got.ModifiedNS != info.ModTime().UnixNano() {
		t.Fatalf("capture stat = %+v, does not describe the file it read", got)
	}
	raw, err := store.GetSHA(got.SHA256)
	testutil.FailErr(t, "read object", err)
	if string(raw) != "package main\n" {
		t.Fatalf("round-tripped content = %q", raw)
	}
}

func TestEmptyContentIsStoredAsAReadableObject(t *testing.T) {
	store := New(t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	testutil.FailErr(t, "write empty source", os.WriteFile(path, nil, 0o644))

	got, err := store.PutFile(t.Context(), path)
	testutil.FailErr(t, "capture empty source", err)
	if got.Size != 0 {
		t.Fatalf("plain size = %d, want 0", got.Size)
	}
	// Streaming and in-memory captures use the same encoding for empty input.
	canonical, err := zstdcodec.Compress(bytes.NewReader(nil))
	testutil.FailErr(t, "compress empty reference", err)
	if got.Stored != int64(len(canonical)) {
		t.Fatalf("stored size = %d, want the codec's canonical empty stream (%d)", got.Stored, len(canonical))
	}
	raw, err := store.GetSHA(got.SHA256)
	testutil.FailErr(t, "read empty object", err)
	if len(raw) != 0 {
		t.Fatalf("round-tripped content = %q, want empty", raw)
	}
}

func TestPutFileReusesAnAlreadyStoredObject(t *testing.T) {
	store := New(t.TempDir())
	dir := t.TempDir()
	first := filepath.Join(dir, "a.go")
	second := filepath.Join(dir, "b.go")
	testutil.FailErr(t, "write a", os.WriteFile(first, []byte("same\n"), 0o644))
	testutil.FailErr(t, "write b", os.WriteFile(second, []byte("same\n"), 0o644))

	a, err := store.PutFile(t.Context(), first)
	testutil.FailErr(t, "capture a", err)
	before, err := os.Stat(filepath.Join(store.Root(), a.Rel))
	testutil.FailErr(t, "stat object", err)

	b, err := store.PutFile(t.Context(), second)
	testutil.FailErr(t, "capture b", err)
	if a.SHA256 != b.SHA256 || a.Rel != b.Rel {
		t.Fatalf("identical content stored twice: %q and %q", a.Rel, b.Rel)
	}
	after, err := os.Stat(filepath.Join(store.Root(), a.Rel))
	testutil.FailErr(t, "restat object", err)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an already-stored object was rewritten")
	}
}

func TestSameFileVersionRejectsARewrittenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "moving.go")
	testutil.FailErr(t, "write source", os.WriteFile(path, []byte("first\n"), 0o644))
	before, err := os.Lstat(path)
	testutil.FailErr(t, "stat before", err)

	if !sameFileVersion(before, before) {
		t.Fatal("one unchanged stat compared unequal to itself")
	}
	for _, rewrite := range []struct {
		name  string
		apply func()
	}{
		{"different length", func() {
			testutil.FailErr(t, "rewrite longer", os.WriteFile(path, []byte("second-and-longer\n"), 0o644))
		}},
		{"same length", func() {
			testutil.FailErr(t, "rewrite same length", os.WriteFile(path, []byte("firsT\n"), 0o644))
		}},
		{"replaced inode", func() {
			other := filepath.Join(dir, "other.go")
			testutil.FailErr(t, "write replacement", os.WriteFile(other, []byte("first\n"), 0o644))
			testutil.FailErr(t, "replace", os.Rename(other, path))
		}},
	} {
		t.Run(rewrite.name, func(t *testing.T) {
			rewrite.apply()
			after, err := os.Lstat(path)
			testutil.FailErr(t, "stat after", err)
			if sameFileVersion(before, after) {
				t.Fatal("a rewritten file compared equal to its earlier stat")
			}
		})
	}
}

func TestPruneOrphansKeepsLiveObjectsAndClearsStaging(t *testing.T) {
	store := New(t.TempDir())
	keepRel, _, _, err := store.Put(ContentSHA([]byte("keep\n")), []byte("keep\n"))
	testutil.FailErr(t, "store kept object", err)
	dropRel, _, _, err := store.Put(ContentSHA([]byte("drop\n")), []byte("drop\n"))
	testutil.FailErr(t, "store dropped object", err)

	staging := filepath.Join(store.Root(), stagingDirName)
	testutil.FailErr(t, "make staging", os.MkdirAll(staging, 0o700))
	leftover := filepath.Join(staging, ".capture-leftover")
	testutil.FailErr(t, "write leftover", os.WriteFile(leftover, []byte("partial"), 0o600))
	settled := time.Now().Add(-2 * orphanGracePeriod)
	testutil.FailErr(t, "age dropped object", os.Chtimes(filepath.Join(store.Root(), dropRel), settled, settled))
	testutil.FailErr(t, "age staged object", os.Chtimes(leftover, settled, settled))

	for range orphanShardCount {
		_, err := store.PruneOrphansBatch(func(rel string) (bool, error) {
			return rel == filepath.Clean(keepRel), nil
		})
		testutil.FailErr(t, "prune orphan batch", err)
	}

	if kept, err := store.Exists(keepRel); err != nil || !kept {
		t.Fatalf("live object removed: exists=%v err=%v", kept, err)
	}
	if dropped, err := store.Exists(dropRel); err != nil || dropped {
		t.Fatalf("unreferenced object survived: exists=%v err=%v", dropped, err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("interrupted capture left staged bytes behind: %v", err)
	}
}

func TestPruneOrphansBoundsOneShardPass(t *testing.T) {
	store := New(t.TempDir())
	shard := filepath.Join(store.Root(), "00")
	testutil.FailErr(t, "create object shard", os.MkdirAll(shard, 0o700))
	settled := time.Now().Add(-2 * orphanGracePeriod)
	for i := range orphanBatchSize + 1 {
		path := filepath.Join(shard, fmt.Sprintf("%062x.zst", i))
		testutil.FailErr(t, "write orphan", os.WriteFile(path, []byte("orphan"), 0o600))
		testutil.FailErr(t, "age orphan", os.Chtimes(path, settled, settled))
	}
	checked := 0
	check := func(string) (bool, error) {
		checked++
		return false, nil
	}
	first, err := store.PruneOrphansBatch(check)
	testutil.FailErr(t, "prune first batch", err)
	if checked != orphanBatchSize || first.Checked != orphanBatchSize || first.Removed != orphanBatchSize {
		t.Fatalf("first batch checked %d, reported %+v, want %d", checked, first, orphanBatchSize)
	}
	second, err := store.PruneOrphansBatch(check)
	testutil.FailErr(t, "prune second batch", err)
	if checked != orphanBatchSize+1 || second.Checked != 1 {
		t.Fatalf("checked %d objects after second batch, reported %+v", checked, second)
	}
}

func TestOrphanBatchesReportOneCompleteCycleAcrossEveryShard(t *testing.T) {
	store := New(t.TempDir())
	settled := time.Now().Add(-2 * orphanGracePeriod)
	for _, shard := range []string{"00", "7f", lastOrphanShard} {
		dir := filepath.Join(store.Root(), shard)
		testutil.FailErr(t, "create object shard", os.MkdirAll(dir, 0o700))
		for i := range orphanBatchSize*2 + 1 {
			path := filepath.Join(dir, fmt.Sprintf("%062x.zst", i))
			testutil.FailErr(t, "write orphan", os.WriteFile(path, []byte("orphan"), 0o600))
			testutil.FailErr(t, "age orphan", os.Chtimes(path, settled, settled))
		}
	}
	checked, batches := 0, 0
	for {
		pass, err := store.PruneOrphansBatch(func(string) (bool, error) {
			checked++
			return false, nil
		})
		testutil.FailErr(t, "prune batch", err)
		batches++
		if pass.CycleComplete {
			break
		}
		if batches > orphanShardCount*4 {
			t.Fatal("a full cycle never reported completion")
		}
	}
	if want := 3 * (orphanBatchSize*2 + 1); checked != want {
		t.Fatalf("checked = %d, want %d", checked, want)
	}
	for _, shard := range []string{"00", "7f", lastOrphanShard} {
		entries, err := os.ReadDir(filepath.Join(store.Root(), shard))
		testutil.FailErr(t, "read swept shard", err)
		if len(entries) != 0 {
			t.Fatalf("shard %s kept %d orphans", shard, len(entries))
		}
	}
	// A completed traversal resets the cycle cursor.
	pass, err := store.PruneOrphansBatch(func(string) (bool, error) { return true, nil })
	testutil.FailErr(t, "first batch of the next cycle", err)
	if pass.CycleComplete {
		t.Fatal("a new cycle reported completion on its first batch")
	}
}

func TestPruneOrphansBoundsStagingPass(t *testing.T) {
	store := New(t.TempDir())
	staging := filepath.Join(store.Root(), stagingDirName)
	testutil.FailErr(t, "create staging", os.MkdirAll(staging, 0o700))
	settled := time.Now().Add(-2 * orphanGracePeriod)
	for i := range orphanBatchSize + 1 {
		path := filepath.Join(staging, fmt.Sprintf(".capture-%03d", i))
		testutil.FailErr(t, "write staged orphan", os.WriteFile(path, []byte("partial"), 0o600))
		testutil.FailErr(t, "age staged orphan", os.Chtimes(path, settled, settled))
	}
	checked := 0
	check := func(string) (bool, error) {
		checked++
		return false, nil
	}
	_, err := store.PruneOrphansBatch(check)
	testutil.FailErr(t, "prune first staging batch", err)
	if checked != orphanBatchSize {
		t.Fatalf("checked %d staged objects, want %d", checked, orphanBatchSize)
	}
	_, err = store.PruneOrphansBatch(check)
	testutil.FailErr(t, "prune second staging batch", err)
	if checked != orphanBatchSize+1 {
		t.Fatalf("checked %d staged objects after second batch", checked)
	}
}

func TestResolveRefusesEscapingPaths(t *testing.T) {
	store := New(t.TempDir())
	for _, rel := range []string{"../escape", "/etc/passwd", ".."} {
		if _, err := store.Get(rel); err == nil {
			t.Fatalf("object path %q was accepted", rel)
		}
	}
}

func TestStoreAliasesShareLifecycleBeforeDirectoryCreation(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	testutil.FailErr(t, "create root alias", os.Symlink(root, "alias"))
	canonical := New(filepath.Join(root, "objects"))
	for _, path := range []string{"objects", filepath.Join(root, "alias", "objects")} {
		alias := New(path)
		if alias.root != canonical.root || alias.lifecycle != canonical.lifecycle {
			t.Fatalf("store %q uses a different root or lifecycle", path)
		}
	}
}

type lostGuard struct{}

func (lostGuard) Verify() error {
	return &hostlock.ClaimLostError{StorePath: "store.db", Reason: "store file was replaced"}
}

// A lost claim refuses the maintenance lease.
func TestMaintenanceLeaseRefusesWhenTheStoreClaimIsLost(t *testing.T) {
	store := New(t.TempDir())
	store.SetGuard(lostGuard{})

	release, ok, err := store.TryAcquireMaintenanceLease()
	if ok || release != nil || !errors.Is(err, hostlock.ErrStoreClaimLost) {
		t.Fatalf("lease must be refused with the loss, got ok=%v err=%v", ok, err)
	}
	// A refused lease holds nothing.
	if !store.lifecycle.TryRLock() {
		t.Fatal("a refused maintenance lease left the lifecycle locked")
	}
	store.lifecycle.RUnlock()
}
