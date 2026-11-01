//go:build stress

package mutation

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Keep this diagnostic small enough to identify a per-entry cost before the
// required full-size entry test exercises the final implementation.
func TestRecursiveCopyEntryCost(t *testing.T) {
	h := newHarness(t, nil)
	testutil.FailErr(t, "create source", os.Mkdir(h.path("source"), 0o755))
	for i := range 1000 {
		testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(h.path("source"), fmt.Sprintf("f%04d", i)), []byte("ok\n"), 0o644))
	}
	result := h.run("", "cp", "-r", "source", "target")
	if result.err != nil || result.result.Status != 0 {
		t.Fatalf("copy outcome: %+v, %v, %s", result.result, result.err, result.stderr)
	}
	var mutations, uncommitted, effects int
	testutil.FailErr(t, "count mutations", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_mutations`).Scan(&mutations))
	testutil.FailErr(t, "count uncommitted mutations", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_mutations WHERE status != 'committed'`).Scan(&uncommitted))
	testutil.FailErr(t, "count effects", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_effects WHERE path LIKE 'target/%' AND entry_kind='file'`).Scan(&effects))
	if effects != 1000 || uncommitted != 0 {
		t.Fatalf("1000-file copy mutation_rows=%d uncommitted=%d file_effects=%d", mutations, uncommitted, effects)
	}
	historyAfterCopy, err := h.service.History.State(t.Context(), h.project.ID)
	testutil.FailErr(t, "history after copy", err)
	if historyAfterCopy.Undo != nil {
		t.Fatalf("copy into new destination recorded unnecessary recovery entry: %+v", historyAfterCopy.Undo)
	}
}

func TestRecursiveRemovalEntryCost(t *testing.T) {
	h := newHarness(t, nil)
	const count = 1000
	for i := range count {
		dir := h.path(filepath.Join("source", fmt.Sprintf("b%02d", i%10)))
		testutil.FailErr(t, "create source bucket", os.MkdirAll(dir, 0o755))
		testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d", i)), []byte("ok\n"), 0o644))
	}
	result := h.run("", "rm", "-rf", "source")
	if result.err != nil || result.result.Status != 0 || !h.absent("source") {
		t.Fatalf("remove outcome: %+v error=%v stderr=%s", result.result, result.err, result.stderr)
	}
	slots, err := os.ReadDir(settingsoverlay.RecoveryDir(h.root))
	testutil.FailErr(t, "read recovery slots", err)
	var deleteMutations, deleteEffects, blobs int
	testutil.FailErr(t, "count delete mutations", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_mutations WHERE kind='delete'`).Scan(&deleteMutations))
	testutil.FailErr(t, "count delete effects", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_effects WHERE op='delete'`).Scan(&deleteEffects))
	testutil.FailErr(t, "count blobs", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_blob_objects`).Scan(&blobs))
	if len(slots) != 1 || deleteMutations != 1 || deleteEffects != 1 || blobs != 0 {
		t.Fatalf("1000-file removal slots=%d delete_mutations=%d delete_effects=%d blobs=%d", len(slots), deleteMutations, deleteEffects, blobs)
	}
	h.undoAll(1)
	for i := range count {
		if got := h.read(filepath.Join("source", fmt.Sprintf("b%02d", i%10), fmt.Sprintf("f%04d", i))); got != "ok\n" {
			t.Fatalf("restored file %d=%q", i, got)
		}
	}
}
