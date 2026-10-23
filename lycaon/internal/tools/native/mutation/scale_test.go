package mutation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOversizeDeleteRejectionLeavesTreeIntact(t *testing.T) {
	h := newHarness(t, map[string]string{
		"oversize/a.txt": strings.Repeat("a", 32),
		"oversize/b.txt": strings.Repeat("b", 32),
	})
	h.service.History.SetRecoveryQuotaBytes(16)
	var notes []string
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, ch := range changes {
			if !ch.RecoveryLoss || ch.Preview.BeforeBytes != 64 {
				t.Fatalf("inaccurate permanent removal facts: %+v", ch)
			}
			notes = append(notes, ch.Preview.PreviewNote)
			t.Logf("permanent-removal preview: %s", ch.Preview.PreviewNote)
		}
		return errors.New("person declined permanent deletion")
	}
	r := h.run("", "rm", "-rf", "oversize")
	if r.err == nil || h.absent("oversize/a.txt") || h.absent("oversize/b.txt") {
		t.Fatalf("declined oversize delete should leave tree intact: %+v %v", r.result, r.err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected one permanent-removal review, got %d", len(notes))
	}
}

func TestDirectoryDeletesRespectQuotaAndPruneRetainedTrees(t *testing.T) {
	h := newHarness(t, nil)
	// Measure the real filesystem allocation before setting this fixture
	// quota. Retained trees must be reclaimed without per-file blob capture.

	for round := 0; round < 5; round++ {
		chunk := fmt.Sprintf("round %d\n", round) + strings.Repeat("retained-build-artifact-line\n", 48)
		for _, dir := range []string{"node_modules/pkg", "dist/assets"} {
			full := h.path(filepath.Join(dir, fmt.Sprintf("bundle-%d.js", round)))
			testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(filepath.Dir(full), 0o755))
			testutil.FailErr(t, "write "+full, os.WriteFile(full, []byte(chunk), 0o644))
		}
		if round == 0 {
			measured, err := h.service.InspectRemoval(t.Context(), fseffect.Location{Root: h.root, Rel: "node_modules"})
			testutil.FailErr(t, "measure retained tree allocation", err)
			if !measured.RecoverySizeKnown || measured.RecoveryBytes < 1 {
				t.Fatalf("unavailable fixture allocation: %+v", measured)
			}
			h.service.History.SetRecoveryQuotaBytes(2 * measured.RecoveryBytes)
		}
		if r := h.run("", "rm", "-rf", "node_modules"); r.err != nil || r.result.Status != 0 || !h.absent("node_modules") {
			t.Fatalf("round %d rm node_modules: %+v %v", round, r.result, r.err)
		}
		if r := h.run("", "rm", "-rf", "dist"); r.err != nil || r.result.Status != 0 || !h.absent("dist") {
			t.Fatalf("round %d rm dist: %+v %v", round, r.result, r.err)
		}
	}

	var historyCount int
	testutil.FailErr(t, "count history entries", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_history_entries WHERE project_id = ?`, h.project.ID).Scan(&historyCount))
	if historyCount > 2 {
		t.Fatalf("expected directory history to stay bounded (<= 2 entries), got %d", historyCount)
	}
	var totalRecoveryBytes int64
	testutil.FailErr(t, "sum recovery bytes", h.db.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(size), 0) FROM source_blob_objects`).Scan(&totalRecoveryBytes))
	if totalRecoveryBytes != 0 {
		t.Fatalf("tree removal captured per-file blobs: %d bytes", totalRecoveryBytes)
	}
	ledger := h.tc.Source.SourceLedger.(*sourceledger.Store)
	slots, err := os.ReadDir(ledger.Retention.RecoveryTreeRoot())
	testutil.FailErr(t, "inspect retained recovery slots", err)
	if len(slots) != historyCount {
		t.Fatalf("physical custody not reclaimed with history: slots=%d history=%d", len(slots), historyCount)
	}
}

func TestOversizeDeleteWithoutReviewerLeavesTreeIntact(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/a": "content"})
	h.service.History.SetRecoveryQuotaBytes(1)
	h.tc.Files.FileChangeReview = nil
	r := h.run("", "rm", "-rf", "tree")
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != "SOURCE_RECOVERY_APPROVAL_UNAVAILABLE" || h.absent("tree/a") {
		t.Fatalf("permanent removal escaped review: result=%+v error=%v", r.result, r.err)
	}
}
