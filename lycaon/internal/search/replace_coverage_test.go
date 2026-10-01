package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcescope"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func settledReplacePreview(ctx context.Context, req ReplacePreviewRequest) (ReplacePreviewResult, error) {
	return awaitReplacePreview(ctx, sourcecatalog.Process(), req)
}

func TestReplacePreviewWaitsForRefreshOfReadableGeneration(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := CodeRoot{ProjectID: "p", RootID: "r", Path: t.TempDir()}
	testutil.FailErr(t, "write original match", os.WriteFile(filepath.Join(root.Path, "early.txt"), []byte("old"), 0600))
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	req := ReplacePreviewRequest{Query: TextExpr{Text: "old"}, Replacement: "new", Roots: []CodeRoot{root}}
	_, err := awaitReplacePreview(t.Context(), catalog, req)
	testutil.FailErr(t, "settle original discovery", err)
	release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
	testutil.FailErr(t, "hold refresh metadata", err)
	defer release()
	testutil.FailErr(t, "write late match", os.WriteFile(filepath.Join(root.Path, "late.txt"), []byte("old"), 0600))
	catalog.InvalidateRoot(root.Path, "late.txt")
	result, err := previewReplace(t.Context(), catalog, req)
	testutil.FailErr(t, "preview refreshing discovery", err)
	if result.State != ReplacePreviewPreparing || len(result.Files) != 0 || !hasCoverageIssue(result.Issues, IssueCatalogRefreshing) {
		t.Fatalf("preview published stale file set: %+v", result)
	}
	release()
	result, err = awaitReplacePreview(t.Context(), catalog, req)
	testutil.FailErr(t, "preview refreshed files", err)
	if result.State != ReplacePreviewReady || len(result.Files) != 2 {
		t.Fatalf("refreshed preview: %+v", result)
	}
}

func TestSearchAndReplaceRetainHealthyRootAfterAnotherRootFails(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	healthy := CodeRoot{ProjectID: "p", RootID: "good", Path: t.TempDir()}
	testutil.FailErr(t, "write healthy match", os.WriteFile(filepath.Join(healthy.Path, "match.txt"), []byte("old"), 0600))
	testutil.FailErr(t, "prepare healthy root", catalogtest.AwaitIndex(t.Context(), catalog, healthy.ProjectID, sourcecatalog.Root{ID: healthy.RootID, Path: healthy.Path}))
	roots := []CodeRoot{{ProjectID: "p", RootID: "bad", Path: filepath.Join(t.TempDir(), "absent")}, healthy}
	report, err := (&CodeExecutor{catalog: catalog}).Run(t.Context(), PlanLeg{Code: &CodePlanLeg{Query: TextExpr{Text: "old"}, PathRoots: roots, Lines: true}})
	testutil.FailErr(t, "search healthy root", err)
	if len(report.Hits) != 1 || report.Hits[0].RootID != "good" || !hasCoverageIssue(report.CoverageIssues(ExecutorCode), IssueExecutorError) {
		t.Fatalf("search lost healthy root: %+v", report)
	}
	result, err := awaitReplacePreview(t.Context(), catalog, ReplacePreviewRequest{Query: TextExpr{Text: "old"}, Replacement: "new", Roots: roots})
	testutil.FailErr(t, "preview healthy root", err)
	if result.State != ReplacePreviewLimited || len(result.Files) != 1 || result.Files[0].RootID != "good" || !hasCoverageIssue(result.Issues, IssueExecutorError) {
		t.Fatalf("preview hid root failure: %+v", result)
	}
}

func TestReplacePreviewCancellationDoesNotCancelSharedDiscovery(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	root := CodeRoot{ProjectID: "p", RootID: "r", Path: t.TempDir()}
	testutil.FailErr(t, "write match", os.WriteFile(filepath.Join(root.Path, "match.txt"), []byte("old"), 0600))
	release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
	testutil.FailErr(t, "hold shared discovery", err)
	defer release()
	req := ReplacePreviewRequest{Query: TextExpr{Text: "old"}, Replacement: "new", Roots: []CodeRoot{root}}
	_, err = previewReplace(t.Context(), catalog, req)
	testutil.FailErr(t, "start discovery", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = previewReplace(ctx, catalog, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preview: %v", err)
	}
	release()
	result, err := awaitReplacePreview(t.Context(), catalog, req)
	testutil.FailErr(t, "join shared discovery", err)
	if result.State != ReplacePreviewReady || len(result.Files) != 1 {
		t.Fatalf("shared discovery did not finish: %+v", result)
	}
}

func awaitReplacePreview(ctx context.Context, catalog *sourcecatalog.Catalog, req ReplacePreviewRequest) (ReplacePreviewResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		result, err := previewReplace(ctx, catalog, req)
		if err != nil || result.State != ReplacePreviewPreparing {
			return result, err
		}
		select {
		case <-ctx.Done():
			return ReplacePreviewResult{}, ctx.Err()
		case <-tick.C:
		}
	}
}

type boundedPreviewScope struct{}

func (boundedPreviewScope) Catalog(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{Budgets: sandbox.SurveyBudgets{DirectoryEntries: 2}}})
}

func TestReplacePreviewReportsBudgetOmissionsAfterDiscoveryFinishes(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	testutil.FailErr(t, "create bounded folder", os.Mkdir(filepath.Join(root, "wide"), 0700))
	for _, name := range []string{"a", "b", "c"} {
		testutil.FailErr(t, "write omitted match", os.WriteFile(filepath.Join(root, "wide", name), []byte("old"), 0600))
	}
	testutil.FailErr(t, "write visible match", os.WriteFile(filepath.Join(root, "visible.txt"), []byte("old"), 0600))
	catalog := sourcecatalog.New()
	catalog.SetScopes(boundedPreviewScope{})
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	result, err := awaitReplacePreview(t.Context(), catalog, ReplacePreviewRequest{
		Query: TextExpr{Text: "old"}, Replacement: "new", Roots: []CodeRoot{{ProjectID: "p", RootID: "r", Path: root}},
	})
	testutil.FailErr(t, "preview bounded discovery", err)
	if result.State != ReplacePreviewLimited || result.Truncated || len(result.Files) != 1 || result.Files[0].Path != "visible.txt" {
		t.Fatalf("bounded preview: %+v", result)
	}
	if !hasCoverageIssue(result.Issues, IssueCatalogBounded) {
		t.Fatalf("missing budget explanation: %+v", result.Issues)
	}
}

func TestReplacePreviewPreparesWithoutPublishingPartialFiles(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	testutil.FailErr(t, "write match", os.WriteFile(filepath.Join(root, "match.txt"), []byte("old"), 0600))
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{Lane: root, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
	testutil.FailErr(t, "hold metadata preparation", err)
	req := ReplacePreviewRequest{Query: TextExpr{Text: "old"}, Replacement: "new", Roots: []CodeRoot{{ProjectID: "p", RootID: "r", Path: root}}}
	result, err := previewReplace(t.Context(), catalog, req)
	release()
	testutil.FailErr(t, "preview pending discovery", err)
	if result.State != ReplacePreviewPreparing || len(result.Files) != 0 || !hasCoverageIssue(result.Issues, IssueCatalogWarming) {
		t.Fatalf("pending preview: %+v", result)
	}
	result, err = awaitReplacePreview(t.Context(), catalog, req)
	testutil.FailErr(t, "preview completed discovery", err)
	if result.State != ReplacePreviewReady || len(result.Files) != 1 {
		t.Fatalf("completed preview: %+v", result)
	}
}

func hasCoverageIssue(issues []Issue, reason IssueReason) bool {
	for _, issue := range issues {
		if issue.Reason == reason {
			return true
		}
	}
	return false
}
