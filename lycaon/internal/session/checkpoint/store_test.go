package checkpoint

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func newTestCheckpointStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	store := New(t.TempDir(), dir, sessionstore.NewMemory())
	if store == nil {
		t.Fatal("NewCheckpointStore returned nil for a real state root and dir")
	}
	return store, dir
}

func checkpointBlobPath(t *testing.T, store *Store, sessionID, anchor, digest string) string {
	t.Helper()
	rel, err := sourceblob.RelPath(digest)
	testutil.FailErr(t, "blob path", err)
	return filepath.Join(store.objects.Root(), rel)
}

func writeProjectFile(t *testing.T, projectDir, rel, body string) {
	t.Helper()
	abs := filepath.Join(projectDir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir parent", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(body), 0o644))
}

func TestCheckpointCapturesPreImageOnFirstTouchOnly(t *testing.T) {
	store, dir := newTestCheckpointStore(t)
	writeProjectFile(t, dir, "src/foo.go", "before")

	_, err := store.Open(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture first touch", store.CapturePreImage(t.Context(), "sess", "anchor", "src/foo.go"))

	writeProjectFile(t, dir, "src/foo.go", "after")
	testutil.FailErr(t, "capture second touch", store.CapturePreImage(t.Context(), "sess", "anchor", "src/foo.go"))

	man, err := store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load manifest", err)
	entry, ok := man.Paths["src/foo.go"]
	if !ok {
		t.Fatal("manifest missing the captured path")
	}
	if entry.Op != OpSnapshot {
		t.Fatalf("op = %q, want snapshot", entry.Op)
	}
	body, err := os.ReadFile(checkpointBlobPath(t, store, "sess", "anchor", entry.SHA256))
	testutil.FailErr(t, "read blob", err)

	body, err = zstdcodec.Decompress(body)
	testutil.FailErr(t, "decompress checkpoint", err)
	if string(body) != "before" {
		t.Fatalf("blob = %q, want the pre-turn bytes %q", body, "before")
	}
}

func TestCheckpointRecordsAbsentForPathsCreatedByTheTurn(t *testing.T) {
	store, _ := newTestCheckpointStore(t)
	_, err := store.Open(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture create", store.CapturePreImage(t.Context(), "sess", "anchor", "src/new.go"))

	man, err := store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load manifest", err)
	entry, ok := man.Paths["src/new.go"]
	if !ok {
		t.Fatal("manifest missing the created path")
	}
	if entry.Op != OpAbsent {
		t.Fatalf("op = %q, want absent so restore deletes the file", entry.Op)
	}
}

func TestCheckpointCoversTheProjectOverlay(t *testing.T) {
	store, dir := newTestCheckpointStore(t)
	blueprint := settingsoverlay.DirName() + "/blueprints/feature.md"
	suppressions := settingsoverlay.DirName() + "/ignores.yaml"
	writeProjectFile(t, dir, blueprint, "# plan\n")

	_, err := store.Open(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture blueprint", store.CapturePreImage(t.Context(), "sess", "anchor", blueprint))
	testutil.FailErr(t, "capture new suppression", store.CapturePreImage(t.Context(), "sess", "anchor", suppressions))

	man, err := store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load manifest", err)
	if len(man.Skipped) != 0 {
		t.Fatalf("nothing here is uncapturable, got skipped=%v", man.Skipped)
	}
	if entry := man.Paths[blueprint]; entry.Op != OpSnapshot {
		t.Fatalf("blueprint op = %q, want snapshot so rewind puts the old plan back", entry.Op)
	}
	if entry := man.Paths[suppressions]; entry.Op != OpAbsent {
		t.Fatalf("suppression op = %q, want absent so rewind removes a risk acceptance the turn added", entry.Op)
	}
	blob, err := os.ReadFile(checkpointBlobPath(t, store, "sess", "anchor", man.Paths[blueprint].SHA256))
	testutil.FailErr(t, "read blob", err)

	blob, err = zstdcodec.Decompress(blob)
	testutil.FailErr(t, "decompress checkpoint", err)
	if string(blob) != "# plan\n" {
		t.Fatalf("blob = %q, want the pre-turn blueprint", blob)
	}
}

// Checkpoints stay outside the project tree.
func TestCheckpointWritesNothingIntoTheProjectTree(t *testing.T) {
	store, dir := newTestCheckpointStore(t)
	writeProjectFile(t, dir, ".env", "API_KEY=live-secret")
	before := projectTreeSnapshot(t, dir)

	_, err := store.Open(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture gitignored secret", store.CapturePreImage(t.Context(), "sess", "anchor", ".env"))
	testutil.FailErr(t, "write turn result", os.WriteFile(filepath.Join(dir, ".env"), []byte("API_KEY=rotated"), 0o644))

	man, err := store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load manifest", err)
	if _, ok := man.Paths[".env"]; !ok {
		t.Fatal("capture must still cover a gitignored file — rewind restores what the turn touched")
	}
	if after := projectTreeSnapshot(t, dir); after != before {
		t.Fatalf("checkpointing added engine state to the project tree:\nbefore %v\nafter  %v", before, after)
	}
	blob, err := os.ReadFile(checkpointBlobPath(t, store, "sess", "anchor", man.Paths[".env"].SHA256))
	testutil.FailErr(t, "read blob", err)

	blob, err = zstdcodec.Decompress(blob)
	testutil.FailErr(t, "decompress checkpoint", err)
	if string(blob) != "API_KEY=live-secret" {
		t.Fatalf("blob = %q, want the pre-turn bytes", blob)
	}
}

func TestReconcileCheckpointRootsKeepsAttachedRoots(t *testing.T) {
	stateRoot := t.TempDir()
	liveRoot := t.TempDir()
	orphanRoot := t.TempDir()
	for _, projectDir := range []string{liveRoot, orphanRoot} {
		store := New(stateRoot, projectDir, sessionstore.NewMemory())
		testutil.FailErr(t, "seed checkpoint root", os.MkdirAll(store.root, 0o700))
	}
	removed, err := ReconcileRoots(stateRoot, []string{liveRoot})
	testutil.FailErr(t, "reconcile checkpoint roots", err)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(New(stateRoot, liveRoot, sessionstore.NewMemory()).root); err != nil {
		t.Fatalf("live checkpoint root removed: %v", err)
	}
	if _, err := os.Stat(New(stateRoot, orphanRoot, sessionstore.NewMemory()).root); !os.IsNotExist(err) {
		t.Fatalf("orphan checkpoint root survived: %v", err)
	}
}

func projectTreeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	testutil.FailErr(t, "walk project tree", filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}))
	sort.Strings(paths)
	return strings.Join(paths, "\n")
}

func TestCheckpointSkipsOversizeFileRatherThanMarkingItAbsent(t *testing.T) {
	store, dir := newTestCheckpointStore(t)
	writeProjectFile(t, dir, "big.bin", strings.Repeat("x", checkpointMaxFileBytes+1))
	_, err := store.Open(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture oversize", store.CapturePreImage(t.Context(), "sess", "anchor", "big.bin"))

	man, err := store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load manifest", err)
	if _, ok := man.Paths["big.bin"]; ok {
		// An absent entry would delete the uncaptured file on restore.
		t.Fatal("oversize file must not enter Paths")
	}
	if len(man.Skipped) != 1 || man.Skipped[0] != "big.bin" {
		t.Fatalf("skipped = %v, want [big.bin]", man.Skipped)
	}
}

// A file at the retention bound is captured byte-exact, so a rewind can
// restore every file a native edit may change.
func TestCheckpointCapturesAFileAtTheRetentionBound(t *testing.T) {
	store, dir := newTestCheckpointStore(t)
	body := strings.Repeat("x", sourceblob.MaxRevisionContentBytes)
	writeProjectFile(t, dir, "bound.txt", body)
	_, err := store.Open(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture at bound", store.CapturePreImage(t.Context(), "sess", "anchor", "bound.txt"))

	man, err := store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load manifest", err)
	entry, ok := man.Paths["bound.txt"]
	if !ok || entry.Op != OpSnapshot || entry.Size != int64(len(body)) || len(man.Skipped) != 0 {
		t.Fatalf("entry = %+v skipped = %v, want a full snapshot", entry, man.Skipped)
	}
	captured, err := store.objects.GetSHA(entry.SHA256)
	testutil.FailErr(t, "read captured object", err)
	if string(captured) != body {
		t.Fatal("captured object does not round-trip the pre-turn bytes")
	}
}

func TestCheckpointCapEnforcedAndFlaggedTruncated(t *testing.T) {
	store, dir := newTestCheckpointStore(t)
	man, err := store.Open(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "open checkpoint", err)
	// Seeded absent paths place the next capture at the capacity boundary.
	for i := 0; i < MaxPaths-1; i++ {
		man.Paths["absent-"+strconv.Itoa(i)] = PathEntry{Op: OpAbsent}
	}
	testutil.FailErr(t, "seed near-cap checkpoint", store.write(t.Context(), man))
	writeProjectFile(t, dir, "last.go", "body")
	testutil.FailErr(t, "capture at limit", store.CapturePreImage(t.Context(), "sess", "anchor", "last.go"))
	man, err = store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load exact-limit manifest", err)
	if len(man.Paths) != MaxPaths || man.Truncated || man.Paths["last.go"].Op != OpSnapshot {
		t.Fatalf("exact-limit checkpoint: paths=%d truncated=%t last=%+v", len(man.Paths), man.Truncated, man.Paths["last.go"])
	}
	testutil.FailErr(t, "recapture at limit", store.CapturePreImage(t.Context(), "sess", "anchor", "last.go"))
	man, err = store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load recaptured manifest", err)
	if man.Truncated {
		t.Fatal("recapturing an existing path marked the checkpoint truncated")
	}
	for _, rel := range []string{"overflow.go", "another.go"} {
		testutil.FailErr(t, "capture over limit", store.CapturePreImage(t.Context(), "sess", "anchor", rel))
	}
	man, err = store.Load(t.Context(), "sess", "anchor")
	testutil.FailErr(t, "load truncated manifest", err)
	if len(man.Paths) != MaxPaths || !man.Truncated {
		t.Fatalf("over-limit checkpoint: paths=%d truncated=%t", len(man.Paths), man.Truncated)
	}
}

// Rewind request IDs stay within checkpoint storage.
func TestCheckpointRejectsTraversalIDsRatherThanCleaningThem(t *testing.T) {
	store, dir := newTestCheckpointStore(t)
	writeProjectFile(t, dir, "secret.txt", "do not delete me")

	for _, id := range []string{"..", "../..", "a/b", `a\b`, ".hidden", "/abs", ""} {
		if _, err := store.Open(t.Context(), id, "anchor"); err == nil {
			t.Fatalf("Open accepted unsafe session id %q", id)
		}
		if _, err := store.Open(t.Context(), "sess", id); err == nil {
			t.Fatalf("Open accepted unsafe anchor id %q", id)
		}
		if _, err := store.Load(t.Context(), id, "anchor"); !errors.Is(err, sessionstore.ErrCheckpointMissing) {
			t.Fatalf("Load(%q) = %v, want ErrCheckpointMissing", id, err)
		}
		testutil.FailErr(t, "DropAnchor must be inert for "+id, store.DropAnchor(t.Context(), id, "anchor"))
	}

	if _, err := os.Stat(filepath.Join(dir, "secret.txt")); err != nil {
		t.Fatalf("a traversal id reached outside the checkpoint store: %v", err)
	}
}

func TestCheckpointLoadMissingAnchorIsTyped(t *testing.T) {
	store, _ := newTestCheckpointStore(t)
	if _, err := store.Load(t.Context(), "sess", "nope"); !errors.Is(err, sessionstore.ErrCheckpointMissing) {
		t.Fatalf("err = %v, want ErrCheckpointMissing", err)
	}
}
