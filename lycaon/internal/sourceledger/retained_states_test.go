package sourceledger

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"github.com/lycaon/lycaon/pkg/api"
)

// The tracked baseline carries no effect and must still list.
func TestVersionListIncludesTheTrackedBaseline(t *testing.T) {
	store, ctx := openLedger(t)
	baseline := []byte("one\ntwo\n")
	tracked, err := store.TrackFile(ctx, TrackInput{
		ProjectID: "p1", RootID: "r1", Path: "a.txt",
		Content: baseline,
	})
	testutil.FailErr(t, "track file", err)

	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "a.txt", FileID: tracked.FileID,
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
		OperationID: "edit-a", Before: baseline, After: []byte("one\ntwo edited\n"),
	})

	page, err := store.QueryFileVersions(ctx, "p1", tracked.FileID, 0, 0)
	testutil.FailErr(t, "query versions", err)
	if len(page.Versions) != 2 {
		t.Fatalf("versions = %d want 2 (baseline + edit)", len(page.Versions))
	}
	oldest := page.Versions[len(page.Versions)-1]
	if oldest.ID != tracked.VersionID {
		t.Fatalf("oldest version = %s want the tracked baseline %s", oldest.ID, tracked.VersionID)
	}
	if oldest.ContentSHA256 != sourceblob.ContentSHA(baseline) {
		t.Fatalf("baseline sha = %q want the tracked bytes", oldest.ContentSHA256)
	}
	if oldest.CaptureState != "stored" {
		t.Fatalf("baseline capture = %q want stored", oldest.CaptureState)
	}
	// No effect produced it, so it reports no action, actor, or cause rather
	// than an empty one.
	if oldest.Op != "" || oldest.Origin != "" || oldest.EffectID != "" {
		t.Fatalf("baseline op/origin/effect = %q/%q/%q want all empty",
			oldest.Op, oldest.Origin, oldest.EffectID)
	}
	if page.Versions[0].Op != api.SourceChangeOpWrite {
		t.Fatalf("newest op = %q want write", page.Versions[0].Op)
	}
}

// The pre-image the ledger preserves when a change arrives against an unknown
// head is a restorable state too.
func TestVersionListIncludesAPreservedPreImage(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "b.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		OperationID: "observe-b",
		Before:      []byte("unrecorded\n"), After: []byte("observed\n"),
	})
	fileID, _ := mustResolve(t, store, ctx, "b.txt")
	page, err := store.QueryFileVersions(ctx, "p1", fileID, 0, 0)
	testutil.FailErr(t, "query versions", err)
	if len(page.Versions) != 2 {
		t.Fatalf("versions = %d want 2 (pre-image + write)", len(page.Versions))
	}
	preImage := page.Versions[1]
	if preImage.ContentSHA256 != sourceblob.ContentSHA([]byte("unrecorded\n")) {
		t.Fatalf("pre-image sha = %q want the unrecorded bytes", preImage.ContentSHA256)
	}
}

// Every retained state takes a position on the project's clock, so paging is
// exclusive and terminates however the states were produced.
func TestVersionPagingCoversEveryRetainedState(t *testing.T) {
	store, ctx := openLedger(t)
	tracked, err := store.TrackFile(ctx, TrackInput{
		ProjectID: "p1", RootID: "r1", Path: "c.txt",
		Content: []byte("v0\n"),
	})
	testutil.FailErr(t, "track file", err)
	previous := []byte("v0\n")
	for _, next := range [][]byte{[]byte("v1\n"), []byte("v2\n"), []byte("v3\n")} {
		mustRecord(t, store, ctx, RecordInput{
			ProjectID: "p1", RootID: "r1", Path: "c.txt", FileID: tracked.FileID,
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
			OperationID: "edit-" + string(next), Before: previous, After: next,
		})
		previous = next
	}

	seen := map[string]struct{}{}
	before := int64(0)
	for pages := 0; ; pages++ {
		if pages > 8 {
			t.Fatal("version paging did not terminate")
		}
		page, err := store.QueryFileVersions(ctx, "p1", tracked.FileID, 1, before)
		testutil.FailErr(t, "query versions", err)
		for _, version := range page.Versions {
			if _, dup := seen[version.ID]; dup {
				t.Fatalf("version %s returned twice", version.ID)
			}
			seen[version.ID] = struct{}{}
		}
		if page.NextBeforeOrdinal == 0 {
			break
		}
		if before != 0 && page.NextBeforeOrdinal >= before {
			t.Fatalf("cursor did not advance: %d then %d", before, page.NextBeforeOrdinal)
		}
		before = page.NextBeforeOrdinal
	}
	if len(seen) != 4 {
		t.Fatalf("paged versions = %d want 4 (baseline + 3 edits)", len(seen))
	}
}

// A content state with no identity records as unresolved.
func TestContentWithoutAnIdentityRecordsAsUnresolved(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "opaque.bin",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		OperationID: "observe-opaque",
	})
	fileID, versionID := mustResolve(t, store, ctx, "opaque.bin")
	if fileID == "" || versionID == "" {
		t.Fatal("observation without content produced no tracked state")
	}
	row, err := store.queries.GetSourceVersion(ctx, versionID)
	testutil.FailErr(t, "read version", err)
	if row.State != "unresolved" || row.CaptureReason == "" {
		t.Fatalf("state/reason = %q/%q want unresolved with a reason", row.State, row.CaptureReason)
	}
}

// Retained bytes are served only when they still hash to the identity that
// named them — the diff path and the restore path answer the same way.
func TestUnverifiableBytesAreNeverServedAsHistory(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "d.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-d", After: []byte("trustworthy\n"),
	})
	_, versionID := mustResolve(t, store, ctx, "d.txt")
	row, err := store.queries.GetSourceVersion(ctx, versionID)
	testutil.FailErr(t, "read version", err)
	object, err := store.queries.GetSourceBlobObject(ctx, row.ContentSha256)
	testutil.FailErr(t, "read blob object", err)
	swapRetainedBytes(t, store, object.StorageRelpath)

	side, err := store.comparisonSide(ctx, "p1", versionID)
	testutil.FailErr(t, "comparison side", err)
	if side.Availability != ContentUnavailable {
		t.Fatalf("availability = %q want unavailable for unverifiable bytes", side.Availability)
	}
	if side.Content != "" {
		t.Fatalf("content = %q want empty for unverifiable bytes", side.Content)
	}
	if _, err := store.ReadRestorableVersion(ctx, "p1", versionID); err == nil {
		t.Fatal("restore accepted unverifiable bytes")
	}
}

// Keep compression valid while breaking the content digest.
func swapRetainedBytes(t *testing.T, store *Store, relPath string) {
	t.Helper()
	compressed, err := zstdcodec.Compress(bytes.NewReader([]byte("tampered\n")))
	testutil.FailErr(t, "compress replacement bytes", err)
	target := filepath.Join(store.objects.Root(), relPath)
	testutil.FailErr(t, "swap retained bytes", os.WriteFile(target, compressed, 0o600))
}
