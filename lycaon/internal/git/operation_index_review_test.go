//go:build integration

package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOperationPreparationPreservesIndexWithStaleStatCache(t *testing.T) {
	dir := operationFixture(t)
	operationWrite(t, dir, "local.txt", "untracked local content\n")
	old := time.Unix(1_600_000_000, 0)
	testutil.FailErr(t, "change file metadata", os.Chtimes(filepath.Join(dir, "shared.txt"), old, old))
	index := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(index)
	testutil.FailErr(t, "read original index", err)
	reviewed := false
	operateOK(t, dir, OperationRequest{Kind: "checkout", Action: "detach", Ref: "HEAD", Review: func(_ context.Context, _ []RestoreFile) error {
		reviewed = true
		current, err := os.ReadFile(index)
		testutil.FailErr(t, "read reviewed index", err)
		if !bytes.Equal(before, current) {
			t.Fatal("preparation rewrote the live index")
		}
		return nil
	}})
	if !reviewed {
		t.Fatal("operation did not reach review")
	}
	contents, err := os.ReadFile(filepath.Join(dir, "local.txt"))
	testutil.FailErr(t, "read preserved local file", err)
	if string(contents) != "untracked local content\n" {
		t.Fatalf("local file=%q", contents)
	}
}
