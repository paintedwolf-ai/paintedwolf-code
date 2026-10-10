package sourcecontracts

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceRevisionsResolveTypedSpecsPerRoot(t *testing.T) {
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager)
	repo, root := contractfixture.NestedRootRepo(t)
	p := contractfixture.CreateProjectForTest(t, srv, root)
	base := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	trunk := strings.TrimSpace(gittest.Run(t, repo, "symbolic-ref", "--short", "HEAD"))
	gittest.Run(t, repo, "checkout", "-b", "topic")
	testutil.FailErr(t, "write topic", os.WriteFile(filepath.Join(root, "topic.txt"), []byte("topic\n"), 0o644))
	gittest.CommitAll(t, repo, "topic work")
	topic := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	gittest.Run(t, repo, "checkout", trunk)

	var got wire.SourceRevisionsResponse
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "", url.Values{"spec": {topic[:8]}}, &got); code != 200 {
		t.Fatalf("commit status %d", code)
	}
	want := wire.SourceRevisionComparison{RootID: p.Roots[0].ID, Spec: topic[:8], Kind: "commit", Label: topic[:7] + " topic work",
		BeforeCommit: base, AfterCommit: topic, Subject: "topic work"}
	if len(got.Comparisons) != 1 || got.Comparisons[0] != want {
		t.Fatalf("commit = %+v", got)
	}
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "", url.Values{"spec": {" topic "}, "root_id": {p.Roots[0].ID}}, &got); code != 200 {
		t.Fatalf("branch status %d", code)
	}
	if len(got.Comparisons) != 1 || got.Comparisons[0].Kind != "branch" || got.Comparisons[0].Label != "topic...HEAD" ||
		got.Comparisons[0].BeforeCommit != base || got.Comparisons[0].AfterCommit != base {
		t.Fatalf("branch = %+v", got)
	}
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "", url.Values{"spec": {"no-such-thing"}}, &got); code != 200 || got.Comparisons == nil || len(got.Comparisons) != 0 {
		t.Fatalf("unresolved spec status %d, %+v", code, got)
	}
	for _, q := range []url.Values{{}, {"spec": {""}}, {"spec": {"-p"}}, {"spec": {"topic..--output=x"}}, {"spec": {"topic main"}}} {
		if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "", q, nil); code != 400 {
			t.Fatalf("query %v status %d", q, code)
		}
	}
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "", url.Values{"spec": {"topic"}, "root_id": {"missing"}}, nil); code != 404 {
		t.Fatalf("unknown root status %d", code)
	}
	plain := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
	if code := contractfixture.RequestSourceRevisions(t, srv, plain.ID, "", url.Values{"spec": {"HEAD"}}, &got); code != 200 || len(got.Comparisons) != 0 {
		t.Fatalf("root without Git status %d, %+v", code, got)
	}
}

func TestSourceRevisionReviewPagesRootScopedFilesAndComparesEachSide(t *testing.T) {
	_, database, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	repo, root := contractfixture.NestedRootRepo(t)
	p := contractfixture.CreateProjectForTest(t, srv, root)
	contractfixture.MirrorLedgerProject(t, database, p)
	rootID := p.Roots[0].ID
	base := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	testutil.FailErr(t, "write added", os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644))
	testutil.FailErr(t, "write changed", os.WriteFile(filepath.Join(root, "src", "app.ts"), []byte("changed\n"), 0o644))
	testutil.FailErr(t, "write outside root", os.WriteFile(filepath.Join(repo, "src", "app.ts"), []byte("outside\n"), 0o644))
	gittest.CommitAll(t, repo, "add and change")
	added := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	testutil.FailErr(t, "delete added", os.Remove(filepath.Join(root, "new.txt")))
	gittest.CommitAll(t, repo, "delete")
	deleted := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))

	var page wire.SourceRevisionReview
	q := url.Values{"root_id": {rootID}, "from_revision": {base}, "to_revision": {added}, "limit": {"1"}}
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "/review", q, &page); code != 200 {
		t.Fatalf("review status %d", code)
	}
	if page.RootID != rootID || page.FilesTotal != 2 || len(page.Files) != 1 || page.NextCursor == "" || page.BeforeCommit != base || page.AfterCommit != added {
		t.Fatalf("review = %+v", page)
	}
	q.Set("cursor", page.NextCursor)
	var next wire.SourceRevisionReview
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "/review", q, &next); code != 200 {
		t.Fatalf("next status %d", code)
	}
	if next.NextCursor != "" || len(next.Files) != 1 || next.Files[0].Path == page.Files[0].Path {
		t.Fatalf("next = %+v", next)
	}
	// A cursor continues only the commit pair that issued it.
	otherPair := url.Values{"root_id": {rootID}, "from_revision": {added}, "to_revision": {deleted}, "cursor": {page.NextCursor}}
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "/review", otherPair, nil); code != 400 {
		t.Fatalf("cursor from another commit pair status %d", code)
	}
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "/review", url.Values{"root_id": {rootID}, "to_revision": {base}}, &page); code != 200 {
		t.Fatalf("root commit status %d", code)
	}
	if page.BeforeCommit != "" || page.FilesTotal != 1 || page.Files[0].Path != "src/app.ts" || page.Files[0].Op != wire.SourceChangeOpCreate {
		t.Fatalf("root commit review = %+v", page)
	}
	for _, bad := range []url.Values{
		{"to_revision": {added}}, {"root_id": {rootID}}, {"root_id": {rootID}, "to_revision": {added[:7]}},
		{"root_id": {rootID}, "from_revision": {"HEAD"}, "to_revision": {added}}, {"root_id": {rootID}, "to_revision": {added}, "cursor": {"1"}},
	} {
		if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "/review", bad, nil); code != 400 {
			t.Fatalf("query %v status %d", bad, code)
		}
	}
	if code := contractfixture.RequestSourceRevisions(t, srv, p.ID, "/review", url.Values{"root_id": {rootID}, "to_revision": {strings.Repeat("f", 40)}}, nil); code != 422 {
		t.Fatalf("missing commit status %d", code)
	}

	source := func(before, after, path string) wire.SourceComparisonSelector {
		return wire.SourceComparisonSelector{GitRange: &wire.GitRangeComparisonSource{Kind: "git_range", RootID: rootID, BeforeCommit: before, AfterCommit: after, Path: path}}
	}
	addition := contractfixture.ComparisonViewForTest(t, srv, p.ID, source(base, added, "new.txt"), "after")
	if addition.Comparison == nil || addition.Comparison.Before.State != "absent" || addition.Comparison.Op != wire.SourceChangeOpCreate ||
		contractfixture.ComparisonTextForTest(t, srv, p.ID, addition) != "new\n" {
		t.Fatalf("addition = %+v", addition)
	}
	deletion := contractfixture.ComparisonViewForTest(t, srv, p.ID, source(added, deleted, "new.txt"), "before")
	if deletion.Comparison == nil || deletion.Comparison.After.State != "absent" || deletion.Comparison.Op != wire.SourceChangeOpDelete ||
		contractfixture.ComparisonTextForTest(t, srv, p.ID, deletion) != "new\n" {
		t.Fatalf("deletion = %+v", deletion)
	}
	change := contractfixture.ComparisonViewForTest(t, srv, p.ID, source(base, added, "src/app.ts"), "after")
	if contractfixture.ComparisonTextForTest(t, srv, p.ID, change) != "changed\n" {
		t.Fatal("range comparison did not read the after commit")
	}
	for _, rejected := range []wire.SourceComparisonSelector{
		source(base, added, "../outside.txt"), source(base, added, "missing.txt"), source(base, "HEAD", "new.txt"),
	} {
		if view := contractfixture.ComparisonViewForTest(t, srv, p.ID, rejected, "after"); view.State != "failed" {
			t.Fatalf("accepted %+v", rejected.GitRange)
		}
	}
}
