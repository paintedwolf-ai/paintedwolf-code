package repoinfo_test

import (
	"context"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAnalyzeRootsUsesProviderCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write main.go", err)
	}
	provider := repotest.NewProvider(t)
	ctx := context.Background()
	brief, err := repoinfo.AwaitBrief(ctx, provider, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if !brief.Materialized || brief.FileCount == 0 {
		t.Fatalf("brief = %+v want materialized", brief)
	}

	mrb, err := repoinfo.AnalyzeRoots(ctx, []projectroot.RootRef{{
		Path: dir, IsPrimary: true,
	}}, repoinfo.DefaultBriefBudget(), provider)
	testutil.FailErr(t, "AnalyzeRoots cached", err)
	if mrb.PrimaryRepoBrief().FileCount != brief.FileCount {
		t.Fatalf("cached file count = %d want %d", mrb.PrimaryRepoBrief().FileCount, brief.FileCount)
	}
	roots := mrb.OrientationRoots()
	if len(roots) != 1 || roots[0].Brief.FileCount != brief.FileCount {
		t.Fatalf("materialized orientation roots = %+v", roots)
	}
}
