package sourcecontracts

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGitMovementReviewReadsCommitWithoutFileEffects(t *testing.T) {
	repo, root := contractfixture.NestedRootRepo(t)
	_, database, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	p := contractfixture.CreateProjectForTest(t, srv, root)
	contractfixture.MirrorLedgerProject(t, database, p)
	before := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	testutil.FailErr(t, "write change", os.WriteFile(filepath.Join(root, "src", "app.ts"), []byte("committed change\n"), 0o644))
	testutil.FailErr(t, "write second file", os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644))
	gittest.CommitAll(t, repo, "record two files")
	after := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	contractfixture.RecordGitReviewMovement(t, database, p, "change", "commit", before, after, 1)
	testutil.FailErr(t, "later working change", os.WriteFile(filepath.Join(root, "src", "app.ts"), []byte("later\n"), 0o644))
	var page wire.SourceGitReview
	if code := contractfixture.RequestGitReview(t, srv, p.ID, "change", "review", url.Values{"limit": {"1"}}, &page); code != 200 {
		t.Fatalf("review status %d", code)
	}
	if page.FilesTotal != 2 || len(page.Files) != 1 || page.NextCursor == "" || page.BeforeCommit != before || page.AfterCommit != after || !page.CommitComparison {
		t.Fatalf("review = %+v", page)
	}
	var next wire.SourceGitReview
	if code := contractfixture.RequestGitReview(t, srv, p.ID, "change", "review", url.Values{"limit": {"1"}, "cursor": {page.NextCursor}}, &next); code != 200 {
		t.Fatalf("next status %d", code)
	}
	if next.NextCursor != "" || len(next.Files) != 1 || next.Files[0].Path == page.Files[0].Path {
		t.Fatalf("next = %+v", next)
	}
	source := wire.SourceComparisonSelector{GitChange: &wire.GitChangeComparisonSource{Kind: "git_change", ChangeID: "change", Path: "src/app.ts"}}
	beforeView := contractfixture.ComparisonViewForTest(t, srv, p.ID, source, "before")
	afterView := contractfixture.ComparisonViewForTest(t, srv, p.ID, source, "after")
	if contractfixture.ComparisonTextForTest(t, srv, p.ID, beforeView) != "committed\n" || contractfixture.ComparisonTextForTest(t, srv, p.ID, afterView) != "committed change\n" {
		t.Fatal("comparison did not retain the commit endpoints")
	}
	for _, path := range []string{"../outside.txt", "/absolute", "missing.txt"} {
		source.GitChange.Path = path
		rejected := contractfixture.ComparisonViewForTest(t, srv, p.ID, source, "after")
		if rejected.State != "failed" {
			t.Fatalf("accepted %q", path)
		}
	}
	other := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
	if code := contractfixture.RequestGitReview(t, srv, other.ID, "change", "review", nil, nil); code != 404 {
		t.Fatalf("foreign project status %d", code)
	}
}

func TestGitAmendReviewSeparatesCommitFromMovement(t *testing.T) {
	repo, root := contractfixture.NestedRootRepo(t)
	_, database, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	p := contractfixture.CreateProjectForTest(t, srv, root)
	contractfixture.MirrorLedgerProject(t, database, p)
	parent := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	path := filepath.Join(root, "new.txt")
	testutil.FailErr(t, "write first", os.WriteFile(path, []byte("one\n"), 0o644))
	gittest.CommitAll(t, repo, "first")
	before := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	testutil.FailErr(t, "write amended", os.WriteFile(path, []byte("one\ntwo\n"), 0o644))
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "--amend", "-m", "amended")
	after := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	contractfixture.RecordGitReviewMovement(t, database, p, "amend", "amend", before, after, 1)
	var page wire.SourceGitReview
	if code := contractfixture.RequestGitReview(t, srv, p.ID, "amend", "review", nil, &page); code != 200 {
		t.Fatalf("commit review status %d", code)
	}
	if page.BeforeCommit != parent || page.Insertions != 2 {
		t.Fatalf("commit review = %+v", page)
	}
	if code := contractfixture.RequestGitReview(t, srv, p.ID, "amend", "review", url.Values{"movement": {"true"}}, &page); code != 200 {
		t.Fatalf("movement review status %d", code)
	}
	if page.BeforeCommit != before || page.Insertions != 1 || page.CommitComparison {
		t.Fatalf("movement review = %+v", page)
	}
	for _, query := range []url.Values{{"parent": {"2"}}, {"movement": {"true"}, "parent": {"1"}}, {"movement": {"yes"}}, {"cursor": {"1"}}, {"limit": {"0"}}} {
		if code := contractfixture.RequestGitReview(t, srv, p.ID, "amend", "review", query, nil); code != 400 {
			t.Fatalf("query %v status %d", query, code)
		}
	}
	contractfixture.RecordGitReviewMovement(t, database, p, "missing", "commit", before, strings.Repeat("f", 40), 2)
	if code := contractfixture.RequestGitReview(t, srv, p.ID, "missing", "review", nil, nil); code != 422 {
		t.Fatalf("missing object status %d", code)
	}
}

func TestGitReviewSpecialFileComparisons(t *testing.T) {
	repo, root := contractfixture.NestedRootRepo(t)
	_, database, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	p := contractfixture.CreateProjectForTest(t, srv, root)
	contractfixture.MirrorLedgerProject(t, database, p)
	before := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	testutil.FailErr(t, "write binary", os.WriteFile(filepath.Join(root, "binary"), []byte{0, 1, 2}, 0o644))
	testutil.FailErr(t, "write large blob", os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", projectsource.SourceReadMaxBytes+1)), 0o644))
	testutil.FailErr(t, "write symlink", os.Symlink("../outside-secret", filepath.Join(root, "link")))
	gittest.Run(t, root, "mv", "src/app.ts", "src/renamed.ts")
	gittest.CommitAll(t, repo, "special files")
	after := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	contractfixture.RecordGitReviewMovement(t, database, p, "special", "commit", before, after, 1)
	for _, row := range []struct{ path, availability, reason string }{
		{"binary", "binary", "binary_content"}, {"large", "not_captured", "content_too_large"}, {"link", "available", ""}, {"src/renamed.ts", "available", ""},
	} {
		view := contractfixture.ComparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{GitChange: &wire.GitChangeComparisonSource{Kind: "git_change", ChangeID: "special", Path: row.path}}, "after")
		diff := view.Comparison
		if view.State != "ready" || diff == nil || diff.After == nil || diff.After.Availability != row.availability || diff.After.Reason != row.reason {
			t.Fatalf("path %s: view=%+v", row.path, view)
		}
		if row.path == "link" && contractfixture.ComparisonTextForTest(t, srv, p.ID, view) != "../outside-secret" {
			t.Fatalf("symlink followed: %+v", diff.After)
		}
		if row.path == "src/renamed.ts" && (diff.Before.Path != "src/app.ts" || !diff.LocationChanged || diff.Op != wire.SourceChangeOpRename) {
			t.Fatalf("rename = %+v", diff)
		}
	}
}

func TestGitMergeReviewSelectsEachParent(t *testing.T) {
	repo, root := contractfixture.NestedRootRepo(t)
	_, database, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	p := contractfixture.CreateProjectForTest(t, srv, root)
	contractfixture.MirrorLedgerProject(t, database, p)
	branch := strings.TrimSpace(gittest.Run(t, repo, "symbolic-ref", "--short", "HEAD"))
	gittest.Run(t, repo, "checkout", "-b", "topic")
	testutil.FailErr(t, "write topic", os.WriteFile(filepath.Join(root, "topic.txt"), []byte("topic\n"), 0o644))
	gittest.CommitAll(t, repo, "topic")
	second := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	gittest.Run(t, repo, "checkout", branch)
	testutil.FailErr(t, "write main", os.WriteFile(filepath.Join(root, "main.txt"), []byte("main\n"), 0o644))
	gittest.CommitAll(t, repo, "main")
	first := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	gittest.Run(t, repo, "merge", "--no-ff", "topic", "-m", "merge topic")
	after := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	contractfixture.RecordGitReviewMovement(t, database, p, "merge", "merge", first, after, 1)
	var page wire.SourceGitReview
	if code := contractfixture.RequestGitReview(t, srv, p.ID, "merge", "review", nil, &page); code != 200 {
		t.Fatalf("first parent status %d", code)
	}
	if len(page.Commit.Parents) != 2 || page.BeforeCommit != first || len(page.Files) != 1 || page.Files[0].Path != "topic.txt" {
		t.Fatalf("first parent = %+v", page)
	}
	if code := contractfixture.RequestGitReview(t, srv, p.ID, "merge", "review", url.Values{"parent": {"2"}}, &page); code != 200 {
		t.Fatalf("second parent status %d", code)
	}
	if page.BeforeCommit != second || len(page.Files) != 1 || page.Files[0].Path != "main.txt" {
		t.Fatalf("second parent = %+v", page)
	}
}
