package sourceobservation

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClearDropsAllRebuildableObservations(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "source-observations.db"))
	t.Cleanup(func() { _ = store.Close() })
	testutil.FailErr(t, "put first root", store.Put(t.Context(), "/project/a", []File{{
		Path: "a.go", SHA256: "aaa", GitOID: "111", Size: 3, Mode: 0o644, ModifiedNS: 1,
	}}))
	testutil.FailErr(t, "put second root", store.Put(t.Context(), "/project/b", []File{{
		Path: "b.go", SHA256: "bbb", Size: 3, Mode: 0o644, ModifiedNS: 2,
	}}))
	file, ok, err := store.Get(t.Context(), "/project/a", "a.go")
	testutil.FailErr(t, "get before clear", err)
	if !ok || file.GitOID != "111" {
		t.Fatalf("observation before clear = %+v ok=%v", file, ok)
	}
	testutil.FailErr(t, "clear", store.Clear(t.Context()))
	for _, root := range []string{"/project/a", "/project/b"} {
		_, ok, err := store.Get(t.Context(), root, filepath.Base(root)+".go")
		testutil.FailErr(t, "get "+root, err)
		if ok {
			t.Fatalf("observation for %s survived clear", root)
		}
	}
}

func TestReaderAnswersPointLookups(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "source-observations.db"))
	t.Cleanup(func() { _ = store.Close() })
	testutil.FailErr(t, "put", store.Put(t.Context(), "/project", []File{{
		Path: "a.go", SHA256: "aaa", Size: 3, Mode: 0o644, ModifiedNS: 1,
	}}))
	reader, err := store.Reader(t.Context())
	testutil.FailErr(t, "prepare reader", err)
	t.Cleanup(func() { _ = reader.Close() })
	file, ok, err := reader.Get(t.Context(), "/project", "a.go")
	testutil.FailErr(t, "read", err)
	if !ok || file.SHA256 != "aaa" {
		t.Fatalf("reader observation = %+v ok=%v", file, ok)
	}
	if _, ok, err := reader.Get(t.Context(), "/project", "missing.go"); err != nil || ok {
		t.Fatalf("missing path ok=%v err=%v", ok, err)
	}
}

func TestOpenRebuildsMismatchedCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-observations.db")
	store := New(path)
	testutil.FailErr(t, "put observation", store.Put(t.Context(), "/project", []File{{
		Path: "a.go", SHA256: "aaa", Size: 3, Mode: 0o644, ModifiedNS: 1,
	}}))
	testutil.FailErr(t, "close observation store", store.Close())

	database, err := sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "open raw cache", err)
	_, err = database.Exec(`PRAGMA user_version = 9`)
	testutil.FailErr(t, "change cache version", err)
	testutil.FailErr(t, "close raw cache", database.Close())

	store = New(path)
	t.Cleanup(func() { _ = store.Close() })
	_, ok, err := store.Get(t.Context(), "/project", "a.go")
	testutil.FailErr(t, "read rebuilt cache", err)
	if ok {
		t.Fatal("rebuilt cache still holds the old observation")
	}
}

func TestOpenRebuildsUnreadableCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-observations.db")
	testutil.FailErr(t, "write unreadable cache", os.WriteFile(path, []byte("not sqlite"), 0o600))

	store := New(path)
	t.Cleanup(func() { _ = store.Close() })
	_, ok, err := store.Get(t.Context(), "/project", "a.go")
	testutil.FailErr(t, "read rebuilt cache", err)
	if ok {
		t.Fatal("rebuilt cache holds an observation")
	}
}

func TestProvenancePrivateImmunityAndWatcherOverflow(t *testing.T) {
	ctx := t.Context()
	store := New(filepath.Join(t.TempDir(), "source-observations.db"))
	t.Cleanup(func() { _ = store.Close() })

	root := "/project/root"
	privObs := observation.SourceObservation{
		Path:             layeredfs.QualifiedPath{RootID: "primary", Rel: "priv.go"},
		Layer:            layeredfs.LayerPrivate,
		Provenance:       observation.ProvenanceExactNativeRead,
		Completeness:     observation.CompletenessComplete,
		SHA256:           "sha-private-v1",
		Size:             42,
		ModTimeNS:        100,
		SourceGeneration: 2,
	}
	trunkObs := observation.SourceObservation{
		Path:             layeredfs.QualifiedPath{RootID: "primary", Rel: "trunk.go"},
		Layer:            layeredfs.LayerTrunk,
		Provenance:       observation.ProvenanceIndexDerived,
		Completeness:     observation.CompletenessPartialKernelCached,
		SHA256:           "sha-trunk-v1",
		Size:             10,
		ModTimeNS:        100,
		SourceGeneration: 2,
	}
	testutil.FailErr(t, "record private", store.RecordSourceObservation(ctx, root, privObs, "oid-priv", 0o644))
	testutil.FailErr(t, "record trunk", store.RecordSourceObservation(ctx, root, trunkObs, "oid-trunk", 0o644))

	// Attempt to overwrite private path with a trunk observation must not clobber private layer.
	trunkOverwriteAttempt := privObs
	trunkOverwriteAttempt.Layer = layeredfs.LayerTrunk
	trunkOverwriteAttempt.SHA256 = "sha-trunk-clobber"
	testutil.FailErr(t, "attempt trunk overwrite of private", store.RecordSourceObservation(ctx, root, trunkOverwriteAttempt, "oid-clobber", 0o644))

	gotPriv, ok, err := store.Get(ctx, root, "priv.go")
	testutil.FailErr(t, "get priv.go", err)
	if !ok || gotPriv.Layer != layeredfs.LayerPrivate || gotPriv.SHA256 != "sha-private-v1" || gotPriv.Provenance != observation.ProvenanceExactNativeRead {
		t.Fatalf("private observation clobbered or lost provenance: %+v", gotPriv)
	}

	// Watcher overflow marks trunk observations uncertain while leaving private observations intact.
	testutil.FailErr(t, "record watcher overflow", store.RecordWatcherOverflow(ctx, root, "queue_overflow"))

	gotTrunk, ok, err := store.Get(ctx, root, "trunk.go")
	testutil.FailErr(t, "get trunk.go", err)
	if !ok || !gotTrunk.Uncertain || gotTrunk.Provenance != observation.ProvenanceUncertainOverflow {
		t.Fatalf("expected trunk observation to be marked uncertain on overflow: %+v", gotTrunk)
	}
	if MatchesObservedIdentity(gotTrunk, gotTrunk) {
		t.Fatal("uncertain observation must not vouch for identity")
	}

	gotPrivAfter, ok, err := store.Get(ctx, root, "priv.go")
	testutil.FailErr(t, "get priv.go after overflow", err)
	if !ok || gotPrivAfter.Uncertain || gotPrivAfter.SHA256 != "sha-private-v1" {
		t.Fatalf("private observation affected by trunk watcher overflow: %+v", gotPrivAfter)
	}

	// Same-timestamp, same-size file with different SHA256 must not match identity.
	sameTimeDifferentDigest := gotPrivAfter
	sameTimeDifferentDigest.SHA256 = "sha-different"
	if MatchesObservedIdentity(gotPrivAfter, sameTimeDifferentDigest) {
		t.Fatal("timestamp-only equality must never prove unchanged content")
	}
}

func TestSyncFromTrackerBridgesTrunkChangedNotice(t *testing.T) {
	ctx := t.Context()
	store := New(filepath.Join(t.TempDir(), "source-observations.db"))
	t.Cleanup(func() { _ = store.Close() })

	facts := observation.LiveMountVisibilityFacts("fuse-linux", true, true, true, observation.CompletenessComplete)
	tracker, err := observation.NewTracker(observation.TrackerConfig{ViewID: "view-sync", Facts: facts})
	testutil.FailErr(t, "NewTracker", err)

	qp := layeredfs.QualifiedPath{RootID: "primary", Rel: "watched.go"}
	tracker.RecordServerFSObservation(layeredfs.ObservationEvent{
		Path:   qp,
		Layer:  layeredfs.LayerTrunk,
		Kind:   layeredfs.ObserveRead,
		Size:   12,
		SHA256: "sha-v1",
	})
	tracker.RecordTrunkChange(observation.TrunkChangeEvent{
		Path:      qp,
		Kind:      observation.TrunkWriteInPlace,
		NewSHA256: "sha-v2",
		NewSize:   12,
	})

	notice, err := store.SyncFromTracker(ctx, "/project/sync", "primary", tracker)
	testutil.FailErr(t, "SyncFromTracker", err)
	if notice == nil || notice.EventType != observation.TrunkChangedEventType || notice.GrantsAuthority || notice.EstablishesMergeBase {
		t.Fatalf("unexpected trunk.changed notice from SyncFromTracker: %+v", notice)
	}
	stored, ok, err := store.Get(ctx, "/project/sync", "watched.go")
	testutil.FailErr(t, "Get watched.go", err)
	if !ok || stored.SHA256 != "sha-v1" {
		t.Fatalf("expected synced observation in store, got %+v (ok=%v)", stored, ok)
	}
}
