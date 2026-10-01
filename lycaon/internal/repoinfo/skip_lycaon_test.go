package repoinfo_test

import (
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAnalyzeSkipsLycaonSandboxes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write main.go", err)
	}
	sandbox := filepath.Join(dir, settingsoverlay.DirName(), "worktrees", "stale-job", "nested.go")
	if err := os.MkdirAll(filepath.Dir(sandbox), 0o755); err != nil {
		testutil.FailErr(t, "mkdir sandbox", err)
	}
	if err := os.WriteFile(sandbox, []byte("package nested\n"), 0o644); err != nil {
		testutil.FailErr(t, "write sandbox file", err)
	}

	p := repotest.NewProvider(t)
	brief, err := repoinfo.AwaitBrief(testutil.BoundedContext(t, 5*time.Second), p, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if brief.FileCount != 1 {
		t.Fatalf("FileCount = %d want 1 (overlay sandboxes skipped)", brief.FileCount)
	}
}
