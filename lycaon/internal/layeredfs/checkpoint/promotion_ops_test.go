package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

// promotionOps promotes changed edited files into a target that also holds others untouched files,
// and returns what the preview and the transaction did to the target.
func promotionOps(t *testing.T, changed, others int) (preview, apply map[string]int64) {
	t.Helper()
	primary := t.TempDir()
	for i := range others {
		testutil.FailErr(t, "seed other", os.WriteFile(filepath.Join(primary, fmt.Sprintf("other-%05d.txt", i)), []byte("o\n"), 0o644))
	}
	for i := range changed {
		testutil.FailErr(t, "seed changed", os.WriteFile(filepath.Join(primary, fmt.Sprintf("edit-%03d.txt", i)), []byte("before\n"), 0o644))
	}
	core, err := layeredfs.New(layeredfs.Config{ViewID: "ops", BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}}})
	testutil.FailErr(t, "core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	for i := range changed {
		_, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: fmt.Sprintf("edit-%03d.txt", i)}, []byte("after\n"), 0o644)
		testutil.FailErr(t, "edit", err)
	}
	store, err := NewStore(t.TempDir())
	testutil.FailErr(t, "store", err)
	publisher, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "w", TaskID: "t", ProjectID: "p"})
	testutil.FailErr(t, "publisher", err)
	_, err = publisher.Publish(t.Context(), PublishRequest{CheckpointID: "cp-ops", Trigger: TriggerWorkerCompletion})
	testutil.FailErr(t, "publish", err)
	promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}})
	testutil.FailErr(t, "promoter", err)
	reviewed, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-ops"})
	testutil.FailErr(t, "preview", err)
	preview = promoter.SourceOps().Counts()
	txn, err := BeginTransaction(TransactionConfig{Dir: filepath.Join(t.TempDir(), "tx"), ID: "ops"})
	testutil.FailErr(t, "begin", err)
	_, err = txn.ApplyCheckpoint(t.Context(), promoter, ApplyRequest{Preview: reviewed})
	testutil.FailErr(t, "apply", err)
	testutil.FailErr(t, "commit", txn.Commit())
	apply = txn.SourceOps().Counts()
	for kind, n := range promoter.SourceOps().Counts() {
		apply[kind] += n - preview[kind]
	}
	if got, err := os.ReadFile(filepath.Join(primary, "edit-000.txt")); err != nil || string(got) != "after\n" {
		t.Fatalf("promotion did not land: %q %v", got, err)
	}
	return preview, apply
}

// A promotion's operations on its target depend on the files it changes, not on how many others the
// target holds, and the target root is opened once per promotion, not once per file.
func TestPromotionOperationsScaleWithChangedFilesOnly(t *testing.T) {
	smallPreview, smallApply := promotionOps(t, 8, 10)
	largePreview, largeApply := promotionOps(t, 8, 2000)
	for kind := range mergeKinds(smallApply, largeApply, smallPreview, largePreview) {
		if smallApply[kind] != largeApply[kind] || smallPreview[kind] != largePreview[kind] {
			t.Fatalf("%s: preview %d/%d, apply %d/%d over 10 and 2000 other files", kind, smallPreview[kind], largePreview[kind], smallApply[kind], largeApply[kind])
		}
	}
	// The preview opens the root once; the apply opens it once for its checks and once for its writes.
	if smallPreview["open_root"] != 1 || smallApply["open_root"] != 2 {
		t.Fatalf("target root opened %d times by the preview and %d by the apply: %v %v", smallPreview["open_root"], smallApply["open_root"], smallPreview, smallApply)
	}
	// Doubling the changed files at most doubles the work: nothing is quadratic in the change set.
	_, doubled := promotionOps(t, 16, 10)
	if total(doubled) > 2*total(smallApply) {
		t.Fatalf("16 files cost %d operations, 8 cost %d: %v", total(doubled), total(smallApply), doubled)
	}
	t.Logf("per promotion of 8 files: preview %v apply %v", smallPreview, smallApply)
}

func mergeKinds(maps ...map[string]int64) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range maps {
		for k := range m {
			out[k] = struct{}{}
		}
	}
	return out
}

func total(m map[string]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}
