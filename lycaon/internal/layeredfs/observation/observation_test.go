package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

type testFixture struct {
	t       *testing.T
	trunkA  string
	trunkB  string
	core    *layeredfs.Core
	tracker *Tracker
	events  []InvalidationEvent
}

func newTestFixture(t *testing.T, facts VisibilityFacts) *testFixture {
	t.Helper()
	tmp := t.TempDir()
	trunkA := filepath.Join(tmp, "trunk-a")
	trunkB := filepath.Join(tmp, "trunk-b")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunkA", os.MkdirAll(trunkA, 0o755))
	testutil.FailErr(t, "mkdir trunkB", os.MkdirAll(trunkB, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	roots := []layeredfs.RootSpec{
		{ID: "root-a", Label: "root-a", TrunkPath: trunkA, IsPrimary: true},
		{ID: "root-b", Label: "root-b", TrunkPath: trunkB},
	}
	tracker, err := NewTracker(TrackerConfig{
		ViewID: "view-1665",
		Facts:  facts,
		Roots:  roots,
	})
	testutil.FailErr(t, "NewTracker", err)

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:          "view-1665",
		BackingDir:      backing,
		Roots:           roots,
		ObservationSink: tracker,
	})
	testutil.FailErr(t, "layeredfs.New", err)
	t.Cleanup(func() {
		_ = core.ReleaseView()
	})
	tracker.BindCore(core, roots...)

	fx := &testFixture{
		t:       t,
		trunkA:  trunkA,
		trunkB:  trunkB,
		core:    core,
		tracker: tracker,
	}
	tracker.RegisterInvalidationSink(InvalidationFunc(func(ev InvalidationEvent) {
		fx.events = append(fx.events, ev)
	}))
	return fx
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeTrunkInPlace(t *testing.T, dir, rel string, content []byte) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir parent", os.MkdirAll(filepath.Dir(abs), 0o755))
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	testutil.FailErr(t, "open in-place", err)
	_, wErr := f.Write(content)
	closeErr := f.Close()
	testutil.FailErr(t, "write in-place", errors.Join(wErr, closeErr))
}

func writeTrunkAtomicReplace(t *testing.T, dir, rel string, content []byte) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir parent", os.MkdirAll(filepath.Dir(abs), 0o755))
	tmp := abs + ".tmp-replace"
	testutil.FailErr(t, "write temp", os.WriteFile(tmp, content, 0o644))
	testutil.FailErr(t, "atomic rename", os.Rename(tmp, abs))
}

func TestUnreadReadAndPrivatePathsInPlaceAndAtomicReplacement(t *testing.T) {
	t.Parallel()
	facts := LiveMountVisibilityFacts("nfs-loopback", true, true, true, CompletenessComplete)
	fx := newTestFixture(t, facts)
	qp := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "root-a", Rel: rel}
	}

	writeTrunkInPlace(t, fx.trunkA, "unread.txt", []byte("unread-v1"))
	writeTrunkInPlace(t, fx.trunkA, "read.txt", []byte("read-v1"))
	writeTrunkInPlace(t, fx.trunkA, "private.txt", []byte("private-base-v1"))

	// Read "read.txt" and hold an open descriptor before trunk mutations.
	hid, heldBefore, err := fx.tracker.OpenHeldDescriptor(qp("read.txt"))
	testutil.FailErr(t, "OpenHeldDescriptor", err)
	defer func() { _ = fx.core.CloseHandle(hid) }()

	// Mutate "private.txt" via the worker mount so it has a retained BaseCapture and private content.
	_, err = fx.core.WriteFile(qp("private.txt"), []byte("worker-private-v2"), 0o644)
	testutil.FailErr(t, "worker WriteFile private.txt", err)
	baseBefore, hasBase := fx.core.CapturedBase(qp("private.txt"))
	if !hasBase || baseBefore.SHA256 != sha256Hex([]byte("private-base-v1")) {
		t.Fatalf("expected retained base private-base-v1, got %+v (ok=%v)", baseBefore, hasBase)
	}

	// 1. In-place writes on trunk for unread, read, and private paths.
	writeTrunkInPlace(t, fx.trunkA, "unread.txt", []byte("unread-v2-inplace"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qp("unread.txt"), Kind: TrunkWriteInPlace})

	writeTrunkInPlace(t, fx.trunkA, "read.txt", []byte("read-v2-inplace"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qp("read.txt"), Kind: TrunkWriteInPlace})

	writeTrunkInPlace(t, fx.trunkA, "private.txt", []byte("trunk-clobber-attempt-inplace"))
	privInvs := fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qp("private.txt"), Kind: TrunkWriteInPlace})
	if len(privInvs) != 0 {
		t.Fatalf("expected private path to suppress trunk invalidations, got %+v", privInvs)
	}

	// Verify fresh lookups after in-place trunk writes.
	unreadBytes, err := fx.core.ReadFile(qp("unread.txt"))
	testutil.FailErr(t, "ReadFile unread.txt", err)
	if string(unreadBytes) != "unread-v2-inplace" {
		t.Fatalf("expected unread-v2-inplace, got %q", string(unreadBytes))
	}
	readBytes, err := fx.core.ReadFile(qp("read.txt"))
	testutil.FailErr(t, "ReadFile read.txt", err)
	if string(readBytes) != "read-v2-inplace" {
		t.Fatalf("expected read-v2-inplace, got %q", string(readBytes))
	}

	// 2. Atomic replacements on trunk for unread, read, and private paths.
	writeTrunkAtomicReplace(t, fx.trunkA, "unread.txt", []byte("unread-v3-atomic"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qp("unread.txt"), Kind: TrunkAtomicReplace})

	writeTrunkAtomicReplace(t, fx.trunkA, "read.txt", []byte("read-v3-atomic"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qp("read.txt"), Kind: TrunkAtomicReplace})

	writeTrunkAtomicReplace(t, fx.trunkA, "private.txt", []byte("trunk-clobber-attempt-atomic"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qp("private.txt"), Kind: TrunkAtomicReplace})

	// Held descriptor opened before atomic replacement still reads the unlinked v2 object,
	// while its recorded OpenedDigest reflects v1 at open time and pathname lookup sees v3!
	heldData, heldMeta, err := fx.tracker.ReadHeldDescriptor(hid, 64)
	testutil.FailErr(t, "ReadHeldDescriptor", err)
	if string(heldData) != "read-v2-inplace" {
		t.Fatalf("expected held descriptor to read unlinked in-place object read-v2-inplace, got %q", string(heldData))
	}
	if heldBefore.OpenedDigest != sha256Hex([]byte("read-v1")) {
		t.Fatalf("expected heldBefore.OpenedDigest for read-v1, got %s", heldBefore.OpenedDigest)
	}
	freshReadBytes, err := fx.core.ReadFile(qp("read.txt"))
	testutil.FailErr(t, "fresh ReadFile read.txt", err)
	if string(freshReadBytes) != "read-v3-atomic" {
		t.Fatalf("expected fresh lookup read-v3-atomic, got %q", string(freshReadBytes))
	}
	latestObs, ok := fx.tracker.LatestObservationForPath(qp("read.txt"))
	if !ok || latestObs.Ino == heldMeta.OpenedIno {
		t.Fatalf("expected fresh lookup inode (%d) to differ from held descriptor inode (%d)", latestObs.Ino, heldMeta.OpenedIno)
	}

	// Private content and retained BaseCapture remain untouched by both trunk edits.
	privBytes, err := fx.core.ReadFile(qp("private.txt"))
	testutil.FailErr(t, "ReadFile private.txt", err)
	if string(privBytes) != "worker-private-v2" {
		t.Fatalf("private content overwritten by trunk: %q", string(privBytes))
	}
	baseAfter, hasBaseAfter := fx.core.CapturedBase(qp("private.txt"))
	if !hasBaseAfter || baseAfter.SHA256 != baseBefore.SHA256 {
		t.Fatalf("retained base changed after trunk edits: before=%+v after=%+v", baseBefore, baseAfter)
	}
	baseDiskBytes, err := os.ReadFile(baseAfter.BackingFile)
	testutil.FailErr(t, "read retained base backing file", err)
	if string(baseDiskBytes) != "private-base-v1" {
		t.Fatalf("retained base backing bytes corrupted: %q", string(baseDiskBytes))
	}
}

func TestNegativeLookupsSameSizeChangesAndDirectoryListingsCoherence(t *testing.T) {
	t.Parallel()
	facts := LiveMountVisibilityFacts("fuse-linux", true, true, true, CompletenessComplete)
	fx := newTestFixture(t, facts)
	qp := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "root-a", Rel: rel}
	}

	// 1. Negative lookup followed by create invalidates negative name cache and parent dir.
	attr, err := fx.core.Lstat(qp("missing.txt"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected ENOENT for missing.txt, got %v", err)
	}
	fx.tracker.ObserveLookupResult(qp("missing.txt"), attr, err)

	writeTrunkInPlace(t, fx.trunkA, "missing.txt", []byte("now-present"))
	invs := fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qp("missing.txt"), Kind: TrunkCreate})
	hasNegInv, hasDirInv := false, false
	for _, inv := range invs {
		if inv.Kind == InvalidateNegativeName && inv.ChildName == "missing.txt" {
			hasNegInv = true
		}
		if inv.Kind == InvalidateDirectory && inv.Path.Rel == "." {
			hasDirInv = true
		}
	}
	if !hasNegInv || !hasDirInv {
		t.Fatalf("expected negative-name and directory invalidations on create after ENOENT, got %+v", invs)
	}

	// 2. Native worker edit followed by shell/core read and same-size trunk change with preserved mtime.
	fixedTime := time.Unix(1700000000, 0)
	writeTrunkInPlace(t, fx.trunkA, "samesize.txt", []byte("aaaa-1111-bbb"))
	testutil.FailErr(t, "chtimes v1", os.Chtimes(filepath.Join(fx.trunkA, "samesize.txt"), fixedTime, fixedTime))

	firstRead, err := fx.core.ReadFile(qp("samesize.txt"))
	testutil.FailErr(t, "ReadFile samesize.txt v1", err)
	if string(firstRead) != "aaaa-1111-bbb" {
		t.Fatalf("unexpected v1 content: %q", string(firstRead))
	}

	// Overwrite with identical byte length (13 bytes) and identical timestamp!
	writeTrunkInPlace(t, fx.trunkA, "samesize.txt", []byte("zzzz-2222-ccc"))
	testutil.FailErr(t, "chtimes v2", os.Chtimes(filepath.Join(fx.trunkA, "samesize.txt"), fixedTime, fixedTime))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{
		Path:         qp("samesize.txt"),
		Kind:         TrunkWriteInPlace,
		NewModTimeNS: fixedTime.UnixNano(),
	})

	secondRead, err := fx.core.ReadFile(qp("samesize.txt"))
	testutil.FailErr(t, "ReadFile samesize.txt v2", err)
	if string(secondRead) != "zzzz-2222-ccc" {
		t.Fatalf("expected immediate same-size coherence zzzz-2222-ccc, got %q", string(secondRead))
	}
	notice, ok := fx.tracker.PendingTrunkChangedNotice()
	if !ok || len(notice.AffectedPaths) == 0 {
		t.Fatalf("expected same-size/same-mtime change to be detected via SHA256 digest, got %+v", notice)
	}

	// 3. Worker native edit followed by shell ReadDir / ReadFile is immediately coherent.
	_, err = fx.core.WriteFile(qp("worker-created.txt"), []byte("native-edit"), 0o644)
	testutil.FailErr(t, "WriteFile worker-created.txt", err)
	wInvs := fx.tracker.RecordWorkerMutation(qp("worker-created.txt"), TrunkCreate)
	if len(wInvs) == 0 {
		t.Fatalf("expected worker mutation invalidation events")
	}
	entries, err := fx.core.ReadDir(qp("."))
	testutil.FailErr(t, "ReadDir root", err)
	foundWorkerFile := false
	for _, e := range entries {
		if e.Name == "worker-created.txt" && e.Layer == layeredfs.LayerPrivate {
			foundWorkerFile = true
		}
	}
	if !foundWorkerFile {
		t.Fatalf("expected worker-created.txt in ReadDir listing, got %+v", entries)
	}
}

func TestWatcherOverflowAndVisibilityFactsContract(t *testing.T) {
	t.Parallel()
	if err := ValidateVisibilityFacts(VisibilityFacts{}); !errors.Is(err, ErrInvalidVisibilityFacts) {
		t.Fatalf("expected ErrInvalidVisibilityFacts for zero VisibilityFacts, got %v", err)
	}
	seedFacts := MaterializedSeedVisibilityFacts("")
	if err := ValidateVisibilityFacts(seedFacts); err != nil || seedFacts.UntouchedVisibility != VisibilityFixed {
		t.Fatalf("unexpected materialized seed facts: %+v (err=%v)", seedFacts, err)
	}

	facts := LiveMountVisibilityFacts("fskit-macos", true, true, true, CompletenessPartialKernelCached)
	fx := newTestFixture(t, facts)
	qp := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "root-a", Rel: rel}
	}

	writeTrunkInPlace(t, fx.trunkA, "watched.txt", []byte("watched-content"))
	_, err := fx.core.ReadFile(qp("watched.txt"))
	testutil.FailErr(t, "ReadFile watched.txt", err)

	idxObs := fx.tracker.RecordIndexDerivedObservation(qp("indexed.txt"), sha256Hex([]byte("idx")), 3, 100)
	if idxObs.Provenance != ProvenanceIndexDerived || idxObs.Completeness != CompletenessPartialKernelCached {
		t.Fatalf("expected index-derived partial observation, got %+v", idxObs)
	}

	invs := fx.tracker.RecordWatcherOverflow("root-a", "fsevents_kernel_dropped")
	if len(invs) != 1 || invs[0].Kind != InvalidateAllUncertain {
		t.Fatalf("expected InvalidateAllUncertain on watcher overflow, got %+v", invs)
	}
	latestWatched, ok := fx.tracker.LatestObservationForPath(qp("watched.txt"))
	if !ok || !latestWatched.Uncertain || latestWatched.Provenance != ProvenanceUncertainOverflow {
		t.Fatalf("expected watched.txt observation to become uncertain overflow, got %+v", latestWatched)
	}
}
