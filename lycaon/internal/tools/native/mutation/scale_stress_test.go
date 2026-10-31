//go:build stress

package mutation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRecursiveCopyAndDeleteScale50000Files(t *testing.T) {
	h := newHarness(t, nil)
	const totalFiles = 50000
	const buckets = 100
	srcRoot := h.path("bigtree")
	for b := 0; b < buckets; b++ {
		testutil.FailErr(t, "mkdir bucket", os.MkdirAll(filepath.Join(srcRoot, fmt.Sprintf("b%02d", b)), 0o755))
	}
	payload := []byte("ok\n")
	for i := 0; i < totalFiles; i++ {
		p := filepath.Join(srcRoot, fmt.Sprintf("b%02d", i%buckets), fmt.Sprintf("f%05d.txt", i))
		if err := os.WriteFile(p, payload, 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	var reviews []tools.FileChange
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		reviews = append(reviews, changes...)
		return nil
	}

	cpRun := h.run("", "cp", "-r", "bigtree", "copiedtree")
	if cpRun.err != nil || cpRun.result.Status != 0 || cpRun.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("cp -r 50000 files failed: %+v %v %q", cpRun.result, cpRun.err, cpRun.stderr)
	}
	if got := h.read("copiedtree/b00/f00000.txt"); got != "ok\n" {
		t.Fatalf("copied first file = %q", got)
	}
	if got := h.read("copiedtree/b99/f49999.txt"); got != "ok\n" {
		t.Fatalf("copied last file = %q", got)
	}
	historyAfterCopy, err := h.service.History.State(t.Context(), h.project.ID)
	testutil.FailErr(t, "history after copy", err)
	if historyAfterCopy.Undo != nil {
		t.Fatalf("copy into new destination recorded unnecessary recovery entry: %+v", historyAfterCopy.Undo)
	}

	reviews = nil
	rmRun := h.run("", "rm", "-rf", "copiedtree")
	if rmRun.err != nil || rmRun.result.Status != 0 || !h.absent("copiedtree") {
		t.Fatalf("rm -rf 50000 files failed: %+v %v %q", rmRun.result, rmRun.err, rmRun.stderr)
	}
	if len(reviews) != 1 {
		t.Fatalf("expected 1 review for 50000-file delete, got %d: %+v", len(reviews), reviews)
	}
	historyAfterDelete, err := h.service.History.State(t.Context(), h.project.ID)
	testutil.FailErr(t, "history after delete", err)
	if historyAfterDelete.Undo == nil || historyAfterDelete.Undo.Path != "copiedtree" {
		t.Fatalf("whole tree was not retained as one undo action: %+v", historyAfterDelete)
	}
	if reviews[0].Preview.BeforeBytes != int64(totalFiles*len(payload)) {
		t.Fatalf("reviewed tree size = %d", reviews[0].Preview.BeforeBytes)
	}
}
