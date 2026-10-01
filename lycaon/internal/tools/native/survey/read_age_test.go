package survey

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools/fileage"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

// ageRepo creates tracked files with distinct commit dates.
func ageRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gittest.Init(t, dir)
	commitAt := func(rel, msg string, when time.Time) {
		testutil.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte("x\n"), 0o644))
		gittest.Run(t, dir, "add", rel)
		gittest.CommitAt(t, dir, msg, when)
	}
	commitAt("old.txt", "old", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	commitAt("recent.txt", "recent", time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "write untracked", os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x\n"), 0o644))
	return dir
}

func TestReadAttachesAgeContext(t *testing.T) {
	dir := ageRepo(t)
	prov := fileage.New(git.NewManager())
	// Prewarm makes the asynchronous age distribution deterministic.
	prov.Prewarm(context.Background(), dir)
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Age: prov}

	out, err := tool.Run(context.Background(), map[string]any{"path": "old.txt"}, nativefixture.Context(dir))
	testutil.FailErr(t, "read old.txt", err)
	receipt, ok := surveyreceipt.Parse(out)
	if !ok {
		t.Fatalf("no receipt: %s", out)
	}
	if receipt.Age == nil {
		t.Fatal("expected age context on a tracked file")
	}
	if receipt.Age.OlderThanPct != 50 {
		t.Fatalf("OlderThanPct = %d, want 50", receipt.Age.OlderThanPct)
	}
	if !strings.Contains(receipt.Age.Summary, "older than ~50% of tracked files") {
		t.Fatalf("summary = %q", receipt.Age.Summary)
	}
	if strings.Contains(strings.ToLower(receipt.Age.Summary), "stale") {
		t.Fatalf("summary must not editorialize: %q", receipt.Age.Summary)
	}

	untracked, err := tool.Run(context.Background(), map[string]any{"path": "untracked.txt"}, nativefixture.Context(dir))
	testutil.FailErr(t, "read untracked.txt", err)
	ur, ok := surveyreceipt.Parse(untracked)
	if !ok {
		t.Fatalf("no receipt: %s", untracked)
	}
	if ur.Age != nil {
		t.Fatalf("untracked file should carry no age context, got %#v", ur.Age)
	}
}

func TestReadWithoutAgeProvider(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "hello.txt"), []byte("hi\n"), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)} // Age nil
	out, err := tool.Run(context.Background(), map[string]any{"path": "hello.txt"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read hello.txt", err)
	receipt, ok := surveyreceipt.Parse(out)
	if !ok {
		t.Fatalf("no receipt: %s", out)
	}
	if receipt.Age != nil {
		t.Fatalf("nil provider should yield no age context, got %#v", receipt.Age)
	}
}
