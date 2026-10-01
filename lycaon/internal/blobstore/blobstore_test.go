package blobstore_test

import (
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// recompressiblePayload varies enough that zstd's best-compression level
// packs it tighter than the default level used by PutAt/stage, without being
// so degenerate that every level already hits the same floor.
func recompressiblePayload() string {
	words := strings.Fields(
		"the quick brown fox jumps over lazy dog while a coder writes tests for " +
			"content addressed blobs and recompression passes with lots of " +
			"repeated envelope fields like content tool calls reasoning")
	r := rand.New(rand.NewSource(7))
	var sb strings.Builder
	for i := 0; i < 8000; i++ {
		sb.WriteString(words[r.Intn(len(words))])
		sb.WriteByte(' ')
	}
	return sb.String()
}

func newStore(t *testing.T) blobstore.Store {
	t.Helper()
	return blobstore.Store{Root: t.TempDir(), Dir: "prompt-attachments"}
}

func TestPutStoresContentAddressedAndResolves(t *testing.T) {
	store := newStore(t)
	blob, err := store.Put("notes.txt", strings.NewReader("hello"), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put", err)

	if blob.Size != 5 {
		t.Fatalf("Size = %d want 5", blob.Size)
	}
	if want := "prompt-attachments/" + blob.ID + "/notes.txt"; blob.Rel != want {
		t.Fatalf("Rel = %q want %q", blob.Rel, want)
	}
	resolved, err := store.Resolve(blob.ID)
	testutil.FailErr(t, "resolve", err)
	if resolved.Rel != blob.Rel || resolved.Size != blob.Size {
		t.Fatalf("resolved = %+v want %+v", resolved, blob)
	}
	f, err := store.Open(resolved)
	testutil.FailErr(t, "open", err)
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	testutil.FailErr(t, "read", err)
	if string(got) != "hello" {
		t.Fatalf("body = %q want hello", got)
	}
}

func TestDerivedFilesLiveBesideTheirBlobAndLeaveWithIt(t *testing.T) {
	store := newStore(t)
	blob, err := store.Put("bug.mp4", strings.NewReader("video bytes"), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put", err)
	if _, err := store.OpenDerived(blob.ID, "overview.png"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("a blob with nothing derived should report not found, got %v", err)
	}
	testutil.FailErr(t, "put derived", store.PutDerived(blob.ID, "overview.png", strings.NewReader("sheet"), bytebound.Materialization(1<<20)))
	testutil.FailErr(t, "replace derived", store.PutDerived(blob.ID, "overview.png", strings.NewReader("sheet v2"), bytebound.Materialization(1<<20)))
	f, err := store.OpenDerived(blob.ID, "overview.png")
	testutil.FailErr(t, "open derived", err)
	got, err := io.ReadAll(f)
	_ = f.Close()
	testutil.FailErr(t, "read derived", err)
	if string(got) != "sheet v2" {
		t.Fatalf("derived body = %q", got)
	}
	// The blob itself still resolves to its own file, not the derived one.
	resolved, err := store.Resolve(blob.ID)
	testutil.FailErr(t, "resolve", err)
	if resolved.Name != "bug.mp4" || resolved.Size != int64(len("video bytes")) {
		t.Fatalf("resolved = %+v", resolved)
	}
	if err := store.PutDerived(blob.ID, "../escape.png", strings.NewReader("x"), bytebound.Materialization(1<<20)); err == nil {
		t.Fatal("a derived name with a path separator was accepted")
	}
	if err := store.PutDerived(strings.Repeat("f", 64), "overview.png", strings.NewReader("x"), bytebound.Materialization(1<<20)); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("deriving for a missing blob = %v", err)
	}
	removed, err := store.DiscardStagedBefore(blob.ID, time.Time{})
	testutil.FailErr(t, "discard", err)
	if !removed {
		t.Fatal("blob was not discarded")
	}
	if _, err := os.Stat(filepath.Join(store.Root, "prompt-attachments", blob.ID)); !os.IsNotExist(err) {
		t.Fatalf("the blob directory with its derived files remains: %v", err)
	}
}

func TestPutDedupesIdenticalContent(t *testing.T) {
	store := newStore(t)
	first, err := store.Put("a.txt", strings.NewReader("same"), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put first", err)
	second, err := store.Put("a.txt", strings.NewReader("same"), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put second", err)
	if first.ID != second.ID || first.Rel != second.Rel {
		t.Fatalf("expected one blob, got %q and %q", first.Rel, second.Rel)
	}
}

func TestPutRefusesCorruptContentAddressedEntry(t *testing.T) {
	store := newStore(t)
	first, err := store.Put("a.txt", strings.NewReader("same"), bytebound.Materialization(10))
	testutil.FailErr(t, "put first", err)
	testutil.FailErr(t, "corrupt body", os.WriteFile(filepath.Join(store.Root, filepath.FromSlash(first.Rel)), []byte("evil"), 0o600))
	if _, err := store.Put("a.txt", strings.NewReader("same"), bytebound.Materialization(10)); err == nil {
		t.Fatal("corrupt content-addressed entry was accepted as a deduplicated body")
	}
}

func TestPutAtAtomicallyReplacesSameSizeContent(t *testing.T) {
	store := blobstore.Store{Root: t.TempDir()}
	_, err := store.PutAt("tool-output/result.txt", strings.NewReader("old"), bytebound.Materialization(10))
	testutil.FailErr(t, "put old", err)
	_, err = store.PutAt("tool-output/result.txt", strings.NewReader("new"), bytebound.Materialization(10))
	testutil.FailErr(t, "put new", err)
	raw, err := os.ReadFile(filepath.Join(store.Root, "tool-output", "result.txt"))
	testutil.FailErr(t, "read replacement", err)
	got, err := zstdcodec.Decompress(raw)
	testutil.FailErr(t, "decompress replacement", err)
	if string(got) != "new" {
		t.Fatalf("same-size replacement = %q", got)
	}
}

func TestRemoveAtBeforeKeepsRecentReplacement(t *testing.T) {
	store := blobstore.Store{Root: t.TempDir()}
	rel := "tool-output/result.txt"
	_, err := store.PutAt(rel, strings.NewReader("new"), bytebound.Materialization(10))
	testutil.FailErr(t, "put recent body", err)
	removed, err := store.RemoveAtBefore(rel, time.Now().Add(-time.Minute))
	testutil.FailErr(t, "retain recent body", err)
	if removed {
		t.Fatal("recent body was removed")
	}
	settled := time.Now().Add(-2 * time.Minute)
	testutil.FailErr(t, "age body", os.Chtimes(filepath.Join(store.Root, rel), settled, settled))
	removed, err = store.RemoveAtBefore(rel, time.Now().Add(-time.Minute))
	testutil.FailErr(t, "remove settled body", err)
	if !removed {
		t.Fatal("settled body was retained")
	}
}

func TestDiscardStagedBeforeKeepsRecentPublication(t *testing.T) {
	store := newStore(t)
	blob, err := store.Put("body.txt", strings.NewReader("body"), bytebound.Materialization(10))
	testutil.FailErr(t, "put", err)
	var released bool
	store.Released = func(string) error {
		released = true
		return nil
	}
	cutoff := time.Now().Add(-time.Minute)
	removed, err := store.DiscardStagedBefore(blob.ID, cutoff)
	testutil.FailErr(t, "discard recent", err)
	if removed || released {
		t.Fatal("recent publication was released")
	}
	old := time.Now().Add(-2 * time.Minute)
	testutil.FailErr(t, "age publication", os.Chtimes(filepath.Join(store.Root, store.Dir, blob.ID), old, old))
	removed, err = store.DiscardStagedBefore(blob.ID, cutoff)
	testutil.FailErr(t, "discard settled", err)
	if !removed || !released {
		t.Fatal("settled publication was not released")
	}
}

func TestAttachmentCollectionDefersWhileArchiveRetainsBody(t *testing.T) {
	store := newStore(t)
	blob, err := store.Put("body.txt", strings.NewReader("body"), bytebound.Materialization(10))
	testutil.FailErr(t, "put attachment", err)
	old := time.Now().Add(-2 * time.Minute)
	testutil.FailErr(t, "age attachment", os.Chtimes(filepath.Join(store.Root, store.Dir, blob.ID), old, old))
	var released bool
	store.Released = func(string) error { released = true; return nil }
	store.MaintenanceLease = func() (func(), bool) { return nil, false }
	removed, err := store.DiscardStagedBefore(blob.ID, time.Now().Add(-time.Minute))
	testutil.FailErr(t, "defer attachment collection", err)
	if removed || released {
		t.Fatal("attachment collection changed retained bytes or metadata")
	}
	_, err = store.Resolve(blob.ID)
	testutil.FailErr(t, "resolve archive-retained attachment", err)
	var unlocked bool
	store.MaintenanceLease = func() (func(), bool) { return func() { unlocked = true }, true }
	removed, err = store.DiscardStagedBefore(blob.ID, time.Now().Add(-time.Minute))
	testutil.FailErr(t, "collect attachment after archive", err)
	if !removed || !released || !unlocked {
		t.Fatal("attachment collection did not complete after archive retention ended")
	}
}

func TestPutSameContentDifferentNamesHasStableIDs(t *testing.T) {
	store := newStore(t)
	jsonBlob, err := store.Put("a.json", strings.NewReader("same"), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put json", err)
	textBlob, err := store.Put("notes.txt", strings.NewReader("same"), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put text", err)
	if jsonBlob.ID == textBlob.ID {
		t.Fatal("filename-sensitive ids collided")
	}
	resolved, err := store.Resolve(textBlob.ID)
	testutil.FailErr(t, "resolve text", err)
	if resolved.Name != "notes.txt" || resolved.Rel != textBlob.Rel {
		t.Fatalf("resolved = %+v want %+v", resolved, textBlob)
	}
}

func TestRetainedBlobSurvivesStagedCollection(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	first, err := store.Put("first.txt", strings.NewReader("12345"), bytebound.Materialization(10))
	testutil.FailErr(t, "put first", err)
	_, err = store.Retain("operation-one", []string{first.ID})
	testutil.FailErr(t, "retain first", err)
	dir := filepath.Join(store.Root, "prompt-attachments", first.ID)
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age retained blob", os.Chtimes(dir, old, old))
	_, err = store.Put("second.txt", strings.NewReader("x"), bytebound.Materialization(10))
	testutil.FailErr(t, "put second", err)
	if _, err := store.Resolve(first.ID); err != nil {
		t.Fatalf("retained blob was collected: %v", err)
	}
}

func TestDurableClaimSurvivesWithoutMarker(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	blob, err := store.Put("retained.txt", strings.NewReader("retained"), bytebound.Materialization(20))
	testutil.FailErr(t, "put retained blob", err)
	store.Retained = func(id string) (bool, error) { return id == blob.ID, nil }
	dir := filepath.Join(store.Root, "prompt-attachments", blob.ID)
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age retained blob", os.Chtimes(dir, old, old))
	err = store.Maintain()
	testutil.FailErr(t, "collect staged blobs", err)
	if _, err := store.Resolve(blob.ID); err != nil {
		t.Fatalf("durably retained blob was collected: %v", err)
	}
}

func TestRetainValidatesBatchBeforeMarkingAnyBlob(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	first, err := store.Put("first.txt", strings.NewReader("12345"), bytebound.Materialization(10))
	testutil.FailErr(t, "put first", err)
	if _, err := store.Retain("operation-one", []string{first.ID, "not-an-id"}); err == nil {
		t.Fatal("mixed valid and invalid retention batch was accepted")
	}
	dir := filepath.Join(store.Root, "prompt-attachments", first.ID)
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age staged blob", os.Chtimes(dir, old, old))
	err = store.Maintain()
	testutil.FailErr(t, "collect staged blobs", err)
	if _, err := store.Resolve(first.ID); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("failed retention batch left the first blob retained: %v", err)
	}
}

func TestReleaseValidatesBatchBeforeRemovingRetention(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	blob, err := store.Put("first.txt", strings.NewReader("12345"), bytebound.Materialization(10))
	testutil.FailErr(t, "put", err)
	_, err = store.Retain("operation", []string{blob.ID})
	testutil.FailErr(t, "retain", err)
	if err := store.Release("operation", []string{blob.ID, "not-an-id"}); err == nil {
		t.Fatal("mixed valid and invalid release batch was accepted")
	}
	dir := filepath.Join(store.Root, "prompt-attachments", blob.ID)
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age retained blob", os.Chtimes(dir, old, old))
	err = store.Maintain()
	testutil.FailErr(t, "collect staged blobs", err)
	if _, err := store.Resolve(blob.ID); err != nil {
		t.Fatalf("failed release batch removed retention: %v", err)
	}
}

func TestReleaseKeepsOtherPromptRetention(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	blob, err := store.Put("first.txt", strings.NewReader("12345"), bytebound.Materialization(10))
	testutil.FailErr(t, "put", err)
	_, err = store.Retain("operation-one", []string{blob.ID})
	testutil.FailErr(t, "retain first operation", err)
	_, err = store.Retain("operation-two", []string{blob.ID})
	testutil.FailErr(t, "retain second operation", err)
	guards, err := os.ReadDir(filepath.Join(store.Root, "prompt-attachments", ".retentions"))
	testutil.FailErr(t, "list admission guards", err)
	if len(guards) != 1 || !guards[0].IsDir() {
		t.Fatalf("admission guards = %v want one blob guard", guards)
	}
	claims, err := os.ReadDir(filepath.Join(store.Root, "prompt-attachments", ".retentions", blob.ID))
	testutil.FailErr(t, "list admission claims", err)
	if len(claims) != 2 {
		t.Fatalf("admission claims = %v want two operation claims", claims)
	}
	testutil.FailErr(t, "release first operation", store.Release("operation-one", []string{blob.ID}))
	dir := filepath.Join(store.Root, "prompt-attachments", blob.ID)
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age retained blob", os.Chtimes(dir, old, old))
	err = store.Maintain()
	testutil.FailErr(t, "collect staged blobs", err)
	if _, err := store.Resolve(blob.ID); err != nil {
		t.Fatalf("second operation's retention was removed: %v", err)
	}
	testutil.FailErr(t, "release second operation", store.Release("operation-two", []string{blob.ID}))
	testutil.FailErr(t, "age released blob", os.Chtimes(dir, old, old))
	err = store.Maintain()
	testutil.FailErr(t, "collect released blob", err)
	if _, err := store.Resolve(blob.ID); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("released blob survived collection: %v", err)
	}
}

func TestAbandonedRetentionClaimExpires(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	store.Retained = func(string) (bool, error) { return false, nil }
	blob, err := store.Put("first.txt", strings.NewReader("12345"), bytebound.Materialization(10))
	testutil.FailErr(t, "put", err)
	_, err = store.Retain("abandoned-operation", []string{blob.ID})
	testutil.FailErr(t, "retain", err)
	markerDir := filepath.Join(store.Root, "prompt-attachments", ".retentions", blob.ID)
	claims, err := os.ReadDir(markerDir)
	testutil.FailErr(t, "list claims", err)
	if len(claims) != 1 {
		t.Fatalf("claims = %v want one", claims)
	}
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age claim", os.Chtimes(filepath.Join(markerDir, claims[0].Name()), old, old))
	testutil.FailErr(t, "age blob", os.Chtimes(filepath.Join(store.Root, "prompt-attachments", blob.ID), old, old))
	testutil.FailErr(t, "collect abandoned claim", store.Maintain())
	if _, err := store.Resolve(blob.ID); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("abandoned blob survived: %v", err)
	}
}

func TestRetainReportsOnlyNewRetentions(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	first, err := store.Put("first.txt", strings.NewReader("first"), bytebound.Materialization(10))
	testutil.FailErr(t, "put first", err)
	second, err := store.Put("second.txt", strings.NewReader("second"), bytebound.Materialization(10))
	testutil.FailErr(t, "put second", err)
	newlyRetained, err := store.Retain("operation", []string{first.ID})
	testutil.FailErr(t, "retain first", err)
	if len(newlyRetained) != 1 || newlyRetained[0] != first.ID {
		t.Fatalf("first newlyRetained = %v want [%s]", newlyRetained, first.ID)
	}
	newlyRetained, err = store.Retain("operation", []string{first.ID, second.ID})
	testutil.FailErr(t, "retain retry", err)
	if len(newlyRetained) != 1 || newlyRetained[0] != second.ID {
		t.Fatalf("retry newlyRetained = %v want [%s]", newlyRetained, second.ID)
	}
	testutil.FailErr(t, "release retry", store.Release("operation", newlyRetained))
	old := time.Now().Add(-2 * time.Hour)
	for _, blob := range []blobstore.Blob{first, second} {
		dir := filepath.Join(store.Root, "prompt-attachments", blob.ID)
		testutil.FailErr(t, "age blob", os.Chtimes(dir, old, old))
	}
	err = store.Maintain()
	testutil.FailErr(t, "collect released blob", err)
	if _, err := store.Resolve(first.ID); err != nil {
		t.Fatalf("existing retention was released: %v", err)
	}
	if _, err := store.Resolve(second.ID); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("new retention survived release: %v", err)
	}
}

func TestPutRejectsOverBoundWithoutStoring(t *testing.T) {
	store := newStore(t)
	_, err := store.Put("big.bin", strings.NewReader(strings.Repeat("x", 100)), bytebound.Materialization(10))
	var tooLarge *blobstore.TooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err = %v want TooLargeError", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(store.Root, "prompt-attachments"))
	testutil.FailErr(t, "read store dir", readErr)
	for _, e := range entries {
		if e.Name() != ".staging" {
			t.Fatalf("rejected body left %q behind", e.Name())
		}
	}
}

func TestPrivateNamesRemainValidBlobFilenames(t *testing.T) {
	store := newStore(t)
	for _, name := range []string{".blob-upload", ".retained", ".staging", ".retentions"} {
		blob, err := store.Put(name, strings.NewReader(name), bytebound.Materialization(100))
		testutil.FailErr(t, "put "+name, err)
		resolved, err := store.Resolve(blob.ID)
		testutil.FailErr(t, "resolve "+name, err)
		if resolved.Name != name {
			t.Fatalf("Resolve(%q).Name = %q", name, resolved.Name)
		}
		testutil.FailErr(t, "maintain "+name, store.Maintain())
	}
}

func TestCrashedStagingFilesExpire(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	stageDir := filepath.Join(store.Root, "prompt-attachments", ".staging")
	testutil.FailErr(t, "create stage dir", os.MkdirAll(stageDir, 0o700))
	residue := filepath.Join(stageDir, "interrupted")
	testutil.FailErr(t, "write residue", os.WriteFile(residue, []byte("12345"), 0o600))

	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age residue", os.Chtimes(residue, old, old))
	if _, err := store.Put("next.txt", strings.NewReader("x"), bytebound.Materialization(10)); err != nil {
		t.Fatalf("expired staging residue was not collected: %v", err)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatalf("staging residue survived: %v", err)
	}
}

func TestExpiredPruningAdvancesInBoundedBatches(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	ids := make([]string, 0, 150)
	for i := 0; i < 150; i++ {
		blob, err := store.Put(
			"file-"+strconv.Itoa(i)+".txt",
			strings.NewReader(strconv.Itoa(i)),
			bytebound.Materialization(10),
		)
		testutil.FailErr(t, "put staged blob", err)
		ids = append(ids, blob.ID)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, id := range ids {
		dir := filepath.Join(store.Root, "prompt-attachments", id)
		testutil.FailErr(t, "age staged blob", os.Chtimes(dir, old, old))
	}

	for i := 0; i < 4; i++ {
		testutil.FailErr(t, "advance expired pruning", store.Maintain())
	}
	for _, id := range ids {
		if _, err := store.Resolve(id); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("expired blob %s survived bounded pruning: %v", id, err)
		}
	}
}

func TestCollectionReleasesDurableMetadata(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	blob, err := store.Put("body.txt", strings.NewReader("body"), bytebound.Materialization(10))
	testutil.FailErr(t, "put blob", err)
	var released string
	store.Released = func(id string) error {
		released = id
		return nil
	}
	dir := filepath.Join(store.Root, "prompt-attachments", blob.ID)
	old := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "age blob", os.Chtimes(dir, old, old))
	testutil.FailErr(t, "collect blob", store.Maintain())
	if released != blob.ID {
		t.Fatalf("released metadata for %q want %q", released, blob.ID)
	}
}

type gatedReader struct {
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *gatedReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return copy(p, "slow"), io.EOF
}

func TestSlowUploadDoesNotBlockOtherStores(t *testing.T) {
	first := newStore(t)
	second := newStore(t)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := first.Put("slow.txt", &gatedReader{started: started, release: release}, bytebound.Materialization(10))
		done <- err
	}()
	<-started

	quick := make(chan error, 1)
	go func() {
		_, err := second.Put("quick.txt", strings.NewReader("quick"), bytebound.Materialization(10))
		quick <- err
	}()
	select {
	case err := <-quick:
		testutil.FailErr(t, "put into independent store", err)
	case <-time.After(time.Second):
		t.Fatal("independent store was blocked by a streaming upload")
	}
	close(release)
	testutil.FailErr(t, "finish slow upload", <-done)
}

func TestPutAcceptsExactlyAtBound(t *testing.T) {
	store := newStore(t)
	if _, err := store.Put("edge.bin", strings.NewReader(strings.Repeat("x", 10)), bytebound.Materialization(10)); err != nil {
		t.Fatalf("body at the bound must be accepted, got %v", err)
	}
}

func TestResolveRejectsNonDigestIDs(t *testing.T) {
	store := newStore(t)
	for _, id := range []string{"", "..", "../../etc/passwd", "/etc/passwd", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		if _, err := store.Resolve(id); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("Resolve(%q) err = %v want ErrNotFound", id, err)
		}
	}
}

func TestOpenUsesResolvedBlobPath(t *testing.T) {
	store := newStore(t)
	blob, err := store.Put("body.txt", strings.NewReader("body"), bytebound.Materialization(10))
	testutil.FailErr(t, "put", err)
	blob.Rel = "../../outside"
	f, err := store.Open(blob)
	testutil.FailErr(t, "open", err)
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	testutil.FailErr(t, "read", err)
	if string(raw) != "body" {
		t.Fatalf("body = %q want body", raw)
	}
}

func TestSafeNameReducesToLeaf(t *testing.T) {
	for in, want := range map[string]string{
		"a/b/c.txt":                  "c.txt",
		`a\b\c.txt`:                  "c.txt",
		"../../../../etc/passwd":     "passwd",
		"https://example.com/x.json": "x.json",
		"":                           "blob",
		"..":                         "blob",
		".":                          "blob",
		"plain.txt":                  "plain.txt",
	} {
		if got := blobstore.SafeName(in); got != want {
			t.Fatalf("SafeName(%q) = %q want %q", in, got, want)
		}
	}
}

func TestPutWithoutStoreReportsNoStore(t *testing.T) {
	var store blobstore.Store
	if _, err := store.Put("x.txt", strings.NewReader("x"), bytebound.Materialization(10)); !errors.Is(err, blobstore.ErrNoStore) {
		t.Fatalf("err = %v want ErrNoStore", err)
	}
}

func TestAttachmentOperationsWithoutStoreReportNoStore(t *testing.T) {
	var store blobstore.Store
	id := strings.Repeat("a", 64)
	checks := []struct {
		name string
		run  func() error
	}{
		{"retain", func() error { _, err := store.Retain("operation", []string{id}); return err }},
		{"release", func() error { return store.Release("operation", []string{id}) }},
		{"discard", func() error { _, err := store.DiscardStagedBefore(id, time.Time{}); return err }},
		{"maintenance", store.Maintain},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, blobstore.ErrNoStore) {
				t.Fatalf("err = %v want ErrNoStore", err)
			}
		})
	}
}

func TestPutCompressesOnDiskButResolvesPlaintextSize(t *testing.T) {
	store := newStore(t)
	plain := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 2000)
	blob, err := store.Put("body.txt", strings.NewReader(plain), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put", err)
	if blob.Size != int64(len(plain)) {
		t.Fatalf("blob.Size = %d want plaintext length %d", blob.Size, len(plain))
	}

	raw, err := os.ReadFile(filepath.Join(store.Root, filepath.FromSlash(blob.Rel)))
	testutil.FailErr(t, "read on-disk body", err)
	if len(raw) >= len(plain) {
		t.Fatalf("on-disk body did not shrink: %d bytes for %d bytes of input", len(raw), len(plain))
	}
	decoded, err := zstdcodec.Decompress(raw)
	testutil.FailErr(t, "decompress on-disk body", err)
	if string(decoded) != plain {
		t.Fatal("on-disk body did not decompress back to the original plaintext")
	}

	resolved, err := store.Resolve(blob.ID)
	testutil.FailErr(t, "resolve", err)
	if resolved.Size != int64(len(plain)) {
		t.Fatalf("resolved.Size = %d want plaintext length %d", resolved.Size, len(plain))
	}
}

func TestOpenDecompressesTransparently(t *testing.T) {
	store := newStore(t)
	plain := strings.Repeat("payload ", 500)
	blob, err := store.Put("body.txt", strings.NewReader(plain), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put", err)

	rc := mustOpen(t, store, blob)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	testutil.FailErr(t, "read", err)
	if string(got) != plain {
		t.Fatal("Open() did not transparently decompress the stored body")
	}
}

func mustOpen(t *testing.T, store blobstore.Store, blob blobstore.Blob) io.ReadCloser {
	t.Helper()
	rc, err := store.Open(blob)
	testutil.FailErr(t, "open", err)
	return rc
}

func TestPutDedupesIdenticalContentDespiteCompression(t *testing.T) {
	store := newStore(t)
	plain := strings.Repeat("dedupe me\n", 1000)
	first, err := store.Put("a.txt", strings.NewReader(plain), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put first", err)
	second, err := store.Put("a.txt", strings.NewReader(plain), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "put second", err)
	if first.ID != second.ID || first.Rel != second.Rel {
		t.Fatalf("expected one blob, got %q and %q", first.Rel, second.Rel)
	}
	f, err := store.Open(second)
	testutil.FailErr(t, "open", err)
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	testutil.FailErr(t, "read", err)
	if string(got) != plain {
		t.Fatal("deduped blob did not read back the original plaintext")
	}
}

func TestRecompressShrinksAndRoundTripsIdenticalPlaintext(t *testing.T) {
	store := blobstore.Store{Root: t.TempDir()}
	plain := recompressiblePayload()
	blob, err := store.PutAt("model-content/aa/bb", strings.NewReader(plain), bytebound.Materialization(int64(len(plain))))
	testutil.FailErr(t, "put", err)

	before, err := os.ReadFile(filepath.Join(store.Root, filepath.FromSlash(blob.Rel)))
	testutil.FailErr(t, "read before recompress", err)

	changed, err := store.Recompress(blob)
	testutil.FailErr(t, "recompress", err)
	if !changed {
		t.Fatal("expected recompression to shrink this payload")
	}

	after, err := os.ReadFile(filepath.Join(store.Root, filepath.FromSlash(blob.Rel)))
	testutil.FailErr(t, "read after recompress", err)
	if len(after) >= len(before) {
		t.Fatalf("on-disk size did not shrink: before %d, after %d", len(before), len(after))
	}

	decoded, err := zstdcodec.Decompress(after)
	testutil.FailErr(t, "decompress recompressed body", err)
	if string(decoded) != plain {
		t.Fatal("recompression changed the decoded plaintext")
	}
}

func TestRecompressReportsNoChangeWhenAlreadyOptimal(t *testing.T) {
	store := blobstore.Store{Root: t.TempDir()}
	plain := recompressiblePayload()
	blob, err := store.PutAt("model-content/aa/bb", strings.NewReader(plain), bytebound.Materialization(int64(len(plain))))
	testutil.FailErr(t, "put", err)

	changed, err := store.Recompress(blob)
	testutil.FailErr(t, "first recompress", err)
	if !changed {
		t.Fatal("expected the first recompression pass to shrink this payload")
	}

	changed, err = store.Recompress(blob)
	testutil.FailErr(t, "second recompress", err)
	if changed {
		t.Fatal("recompressing an already best-compressed body reported a change")
	}
}

func TestRecompressWithoutStoreReportsNoStore(t *testing.T) {
	store := blobstore.Store{}
	if _, err := store.Recompress(blobstore.Blob{Rel: "model-content/aa/bb"}); !errors.Is(err, blobstore.ErrNoStore) {
		t.Fatalf("expected ErrNoStore, got %v", err)
	}
}
