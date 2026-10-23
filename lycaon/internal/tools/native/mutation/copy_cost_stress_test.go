//go:build stress

package mutation

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	started := time.Now()
	result := h.run("", "cp", "-r", "source", "target")
	elapsed := time.Since(started)
	if result.err != nil || result.result.Status != 0 {
		t.Fatalf("copy outcome: %+v, %v, %s", result.result, result.err, result.stderr)
	}
	var mutations, effects int
	testutil.FailErr(t, "count mutations", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_mutations`).Scan(&mutations))
	testutil.FailErr(t, "count effects", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_effects`).Scan(&effects))
	t.Logf("1000-file copy elapsed=%s mutation_rows=%d effect_rows=%d", elapsed, mutations, effects)
}
