package mutation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRemovalReviewCannotApproveChangedTree(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "growth", true: "replacement"}[replace], func(t *testing.T) {
			h := newHarness(t, map[string]string{"tree/file": "before"})
			h.service.Effects.SetSameFilesystem(func(string, string) (bool, error) { return false, nil })
			calls := 0
			h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
				calls++
				if len(changes) != 1 || !changes[0].RecoveryLoss || changes[0].Preview.BeforeBytes != 6 {
					t.Fatalf("inaccurate review: %+v", changes)
				}
				t.Logf("permanent-removal preview: %s", changes[0].Preview.PreviewNote)
				if replace {
					testutil.FailErr(t, "retain old tree", os.Rename(h.path("tree"), h.path("old-tree")))
					testutil.FailErr(t, "replace tree", os.Mkdir(h.path("tree"), 0o755))
					testutil.FailErr(t, "same bytes in different entry", os.WriteFile(h.path("tree/file"), []byte("before"), 0o644))
				} else {
					testutil.FailErr(t, "change tree during approval", os.WriteFile(h.path("tree/file"), []byte("larger contents"), 0o644))
				}
				return nil
			}
			r := h.run("", "rm", "-rf", "tree")
			if calls != 1 || r.err == nil || h.absent("tree/file") {
				t.Fatalf("review escaped snapshot: calls=%d result=%+v error=%v", calls, r.result, r.err)
			}
			if replace && h.absent("old-tree/file") {
				t.Fatal("original tree was lost")
			}
		})
	}
}

func TestOverwriteAboveRecoveryQuotaRequiresRecoveryLossReview(t *testing.T) {
	h := newHarness(t, map[string]string{"from": "new"})
	original := strings.Repeat("x", sourceledger.MaxRevisionContentBytes+1)
	testutil.FailErr(t, "seed unversioned destination", os.WriteFile(h.path("to"), []byte(original), 0o600))
	h.service.Effects.SetSameFilesystem(func(string, string) (bool, error) { return false, nil })
	lossReviews := 0
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, change := range changes {
			if change.RecoveryLoss {
				lossReviews++
				return errors.New("declined recovery loss")
			}
		}
		return nil
	}
	r := h.run("", "cp", "from", "to")
	if r.err == nil || lossReviews != 1 {
		t.Fatalf("overwrite escaped recovery-loss review: reviews=%d result=%+v error=%v", lossReviews, r.result, r.err)
	}
	got, err := os.ReadFile(filepath.Join(h.root, "to"))
	testutil.FailErr(t, "read preserved destination", err)
	if string(got) != original {
		t.Fatal("declined overwrite changed destination bytes")
	}
}
