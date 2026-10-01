//go:build stress

package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

// wideChangeFiles makes an index delta larger than the literal stdin cap, so a
// count cap or stdin-bound path fails these tests.
const wideChangeFiles = 6000

func writeWideTree(t *testing.T, dir string) {
	t.Helper()
	for i := range wideChangeFiles {
		path := filepath.Join(dir, "wide-change-directory", fmt.Sprintf("generated-file-%05d-with-a-long-name.txt", i))
		if i == 0 {
			testutil.FailErr(t, "create wide directory", os.MkdirAll(filepath.Dir(path), 0o755))
		}
		testutil.FailErr(t, "write wide file", os.WriteFile(path, []byte(fmt.Sprintf("%d\n", i)), 0o644))
	}
}

func TestStressOperationCheckoutHasNoFileCountLimit(t *testing.T) {
	dir := operationFixture(t)
	gittest.Run(t, dir, "checkout", "-b", "wide")
	writeWideTree(t, dir)
	gittest.Run(t, dir, "add", "--", "wide-change-directory")
	gittest.Run(t, dir, "commit", "-m", "Wide")
	gittest.Run(t, dir, "checkout", "main")

	reviewed := 0
	result := operateOK(t, dir, OperationRequest{Kind: "checkout", Branch: "wide", Review: func(_ context.Context, files []RestoreFile) error {
		reviewed = len(files)
		return nil
	}})
	if result.After.Branch != "wide" {
		t.Fatalf("checkout ended on %q", result.After.Branch)
	}
	if reviewed < wideChangeFiles {
		t.Fatalf("review saw %d files, want at least %d", reviewed, wideChangeFiles)
	}
	if len(result.Paths) != wideChangeFiles {
		t.Fatalf("reviewed paths = %d, want %d", len(result.Paths), wideChangeFiles)
	}
	last := filepath.Join(dir, "wide-change-directory", fmt.Sprintf("generated-file-%05d-with-a-long-name.txt", wideChangeFiles-1))
	if _, err := os.Stat(last); err != nil {
		t.Fatalf("checkout did not materialize the last wide file: %v", err)
	}
}

func TestStressOperationStashApplyPublishesAWideIndexDelta(t *testing.T) {
	dir := operationFixture(t)
	writeWideTree(t, dir)
	gittest.Run(t, dir, "add", "--", "wide-change-directory")
	gittest.Run(t, dir, "stash", "push", "-m", "Wide")

	operateOK(t, dir, OperationRequest{Kind: "stash", Action: "apply", Ref: "stash@{0}", ReinstateIndex: true})
	staged := gittest.Run(t, dir, "diff", "--cached", "--name-only", "-z")
	count := 0
	for _, b := range []byte(staged) {
		if b == 0 {
			count++
		}
	}
	if count != wideChangeFiles {
		t.Fatalf("staged after apply = %d, want %d", count, wideChangeFiles)
	}
}
