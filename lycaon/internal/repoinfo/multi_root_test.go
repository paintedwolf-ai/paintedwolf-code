package repoinfo_test

import (
	"context"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAnalyzeRootsEmpty(t *testing.T) {
	mrb, err := repoinfo.AnalyzeRoots(context.Background(), nil, repoinfo.DefaultBriefBudget(), nil)
	testutil.FailErr(t, "AnalyzeRoots empty", err)
	if mrb == nil || len(mrb.Roots) != 0 {
		t.Fatalf("roots = %v want empty", mrb)
	}
}

func TestAnalyzeRootsSingleRootParity(t *testing.T) {
	dir := t.TempDir()
	writeSampleTree(t, dir)
	ctx := context.Background()
	provider := repotest.NewProvider(t)
	defer func() { _ = provider.Close() }()
	direct, err := repoinfo.AwaitBrief(ctx, provider, dir)
	testutil.FailErr(t, "await brief", err)
	single, err := repoinfo.AnalyzeRoots(ctx, []projectroot.RootRef{{
		ID: "r1", Path: dir, IsPrimary: true, Label: "main",
	}}, repoinfo.DefaultBriefBudget(), provider)
	testutil.FailErr(t, "AnalyzeRoots single", err)
	if single.PrimaryRepoBrief().FileCount != direct.FileCount {
		t.Fatalf("file count = %d want %d", single.PrimaryRepoBrief().FileCount, direct.FileCount)
	}
	got := packboard.RepoOrientationLines(single.PrimaryRepoBrief())
	want := packboard.RepoOrientationLines(api.RepoBrief{
		Languages: direct.Languages, FileCount: direct.FileCount, Layout: direct.Layout, GeneratedAt: direct.GeneratedAt,
	})
	if len(got) != len(want) {
		t.Fatalf("lines = %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("line[%d] = %q want %q", i, got[i], want[i])
		}
	}
}

func TestAnalyzeRootsTwoRootComposition(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	secondary := filepath.Join(t.TempDir(), "secondary")
	writeSampleTree(t, primary)
	writeSampleTree(t, secondary)
	if err := os.WriteFile(filepath.Join(secondary, "extra.go"), []byte("package extra\n"), 0o644); err != nil {
		testutil.FailErr(t, "write extra.go", err)
	}
	budget := repoinfo.BriefBudget{TotalBytes: 2048, PrimaryShare: 0.6}
	provider := repotest.NewProvider(t)
	defer func() { _ = provider.Close() }()
	mrb, err := repoinfo.AnalyzeRoots(context.Background(), []projectroot.RootRef{
		{ID: "p", Path: primary, IsPrimary: true, Label: "lycaon"},
		{ID: "s", Path: secondary, IsPrimary: false, Label: "den"},
	}, budget, provider)
	testutil.FailErr(t, "AnalyzeRoots two roots", err)
	if len(mrb.Roots) != 2 {
		t.Fatalf("len = %d want 2", len(mrb.Roots))
	}
	if !mrb.Roots[0].IsPrimary || mrb.Roots[0].Tier != repoinfo.BriefFull {
		t.Fatalf("primary tier = %+v", mrb.Roots[0])
	}
	if mrb.Roots[1].IsPrimary || mrb.Roots[1].Tier != repoinfo.BriefSummary {
		t.Fatalf("secondary tier = %+v", mrb.Roots[1])
	}
	if mrb.RenderedBytes() > budget.TotalBytes+256 {
		t.Fatalf("rendered bytes %d exceed budget %d", mrb.RenderedBytes(), budget.TotalBytes)
	}
}

func writeSampleTree(t *testing.T, root string) {
	t.Helper()
	for _, rel := range []string{"main.go", "internal/foo.go", "README.md"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		body := "package sample\n"
		if strings.HasSuffix(rel, ".md") {
			body = "# doc\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
}
