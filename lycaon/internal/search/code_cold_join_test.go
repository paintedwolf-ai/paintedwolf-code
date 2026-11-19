package search

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// coldRoot returns a root holding needle whose index build cannot admit its
// metadata until release is called, and a catalog that has never seen it.
func coldRoot(t *testing.T) (root string, catalog *sourcecatalog.Catalog, release func()) {
	t.Helper()
	root = t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644))
	hold, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
		Key: "hold:" + root, Lane: root, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
	})
	testutil.FailErr(t, "hold the root's metadata lane", err)
	release = sync.OnceFunc(hold)
	catalog = sourcecatalog.New()
	t.Cleanup(func() {
		release()
		testutil.FailErr(t, "drain search catalog", catalog.Drain(context.Background()))
	})
	return root, catalog, release
}

func coldLeg(root string) PlanLeg {
	return PlanLeg{Code: &CodePlanLeg{
		Query:     TextExpr{Text: "needle"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "cold-root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}}
}

func TestCodeExecutorReportsWarmingRootWhenWallExpires(t *testing.T) {
	root, catalog, _ := coldRoot(t)
	exec := &CodeExecutor{catalog: catalog, wallBudget: 50 * time.Millisecond}
	report, err := exec.Run(t.Context(), coldLeg(root))
	testutil.FailErr(t, "run code leg", err)
	issues := report.CoverageIssues(ExecutorCode)
	if len(report.Hits) != 0 || len(issues) != 1 || issues[0].Reason != IssueCatalogWarming || issues[0].Count != 1 {
		t.Fatalf("issues = %+v report = %+v, want catalog_warming alone", issues, report)
	}
	leg := coldLeg(root)
	leg.Code.PathRoots = append(leg.Code.PathRoots, CodeRoot{ProjectID: "p1", RootID: "after", Path: t.TempDir()})
	report, err = exec.Run(t.Context(), leg)
	testutil.FailErr(t, "run code leg with a root left", err)
	if report.Code.WarmingRoots != 1 || !report.TimedOut {
		t.Fatalf("report = %+v, want warming plus time_budget for the unscanned root", report)
	}
}

func TestCodeExecutorJoinsColdBuildForTheWall(t *testing.T) {
	root, catalog, release := coldRoot(t)
	const held = 500 * time.Millisecond
	time.AfterFunc(held, release)
	report, err := (&CodeExecutor{catalog: catalog}).Run(t.Context(), coldLeg(root))
	testutil.FailErr(t, "run code leg", err)
	if got := hitPathsOfKind(report.Hits, HitKindCode); !contains(got, "a.txt") || report.Code.WarmingRoots != 0 || report.Code.GenerationWait < held {
		t.Fatalf("hits = %v report = %+v, want a.txt after joining the build", got, report)
	}
}

func TestCandidatePagerReportsWarmingRootWhenWallExpires(t *testing.T) {
	root, catalog, _ := coldRoot(t)
	leg := coldLeg(root)
	leg.Code.Candidates = true
	leg.Code.Wall = 50 * time.Millisecond
	report, err := (&CodeExecutor{catalog: catalog}).Run(t.Context(), leg)
	testutil.FailErr(t, "run candidate pager", err)
	if report.Code.WarmingRoots != 1 || report.TimedOut || len(report.Hits) != 0 {
		t.Fatalf("report = %+v, want one warming root and no time_budget", report)
	}
	leg = coldLeg(root)
	leg.Code.Candidates = true
	leg.Code.Wall = 50 * time.Millisecond
	leg.Code.PathRoots = append(leg.Code.PathRoots, CodeRoot{ProjectID: "p1", RootID: "after", Path: t.TempDir()})
	report, err = (&CodeExecutor{catalog: catalog}).Run(t.Context(), leg)
	testutil.FailErr(t, "run candidate pager with a root left", err)
	if report.Code.WarmingRoots != 1 || !report.TimedOut {
		t.Fatalf("report = %+v, want warming plus time_budget for the unscanned root", report)
	}
}

func TestReplacePreviewJoinsColdBuild(t *testing.T) {
	root, catalog, release := coldRoot(t)
	time.AfterFunc(500*time.Millisecond, release)
	result, err := previewReplace(t.Context(), catalog, ReplacePreviewRequest{
		Query: TextExpr{Text: "needle"}, Replacement: "pin",
		Roots: []CodeRoot{{ProjectID: "p1", RootID: "cold-root", Path: root}},
	})
	testutil.FailErr(t, "preview replace", err)
	for _, issue := range result.Issues {
		if issue.Reason == IssueCatalogWarming {
			t.Fatalf("result = %+v, want the cold root joined until readable", result)
		}
	}
}
