package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func recordGitReviewMovement(t *testing.T, database db.Handle, p wire.Project, id, kind, before, after string, ordinal int) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `INSERT INTO source_git_transitions
		(id, project_id, root_id, kind, from_commit, to_commit, ordinal, observed_ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, '2026-09-10T10:00:00Z')`, id, p.ID, p.Roots[0].ID, kind, before, after, ordinal)
	testutil.FailErr(t, "record Git movement", err)
}

func requestGitReview(t *testing.T, srv *Server, projectID, id, suffix string, q url.Values, target any) int {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, newAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/source/git-changes/"+id+"/"+suffix+"?"+q.Encode(), nil))
	if target != nil && w.Code == http.StatusOK {
		testutil.FailErr(t, "decode Git review", json.Unmarshal(w.Body.Bytes(), target))
	}
	return w.Code
}

func TestGitMovementReviewReadsCommitWithoutFileEffects(t *testing.T) {
	repo, root := nestedRootRepo(t)
	_, database, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, root)
	mirrorLedgerProject(t, database, p)
	before := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	testutil.FailErr(t, "write change", os.WriteFile(filepath.Join(root, "src", "app.ts"), []byte("committed change\n"), 0o644))
	testutil.FailErr(t, "write second file", os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644))
	gittest.CommitAll(t, repo, "record two files")
	after := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	recordGitReviewMovement(t, database, p, "change", "commit", before, after, 1)
	testutil.FailErr(t, "later working change", os.WriteFile(filepath.Join(root, "src", "app.ts"), []byte("later\n"), 0o644))
	var page wire.SourceGitReview
	if code := requestGitReview(t, srv, p.ID, "change", "review", url.Values{"limit": {"1"}}, &page); code != 200 {
		t.Fatalf("review status %d", code)
	}
	if page.FilesTotal != 2 || len(page.Files) != 1 || page.NextCursor == "" || page.BeforeCommit != before || page.AfterCommit != after || !page.CommitComparison {
		t.Fatalf("review = %+v", page)
	}
	var next wire.SourceGitReview
	if code := requestGitReview(t, srv, p.ID, "change", "review", url.Values{"limit": {"1"}, "cursor": {page.NextCursor}}, &next); code != 200 {
		t.Fatalf("next status %d", code)
	}
	if next.NextCursor != "" || len(next.Files) != 1 || next.Files[0].Path == page.Files[0].Path {
		t.Fatalf("next = %+v", next)
	}
	source := wire.SourceComparisonSelector{GitChange: &wire.GitChangeComparisonSource{Kind: "git_change", ChangeID: "change", Path: "src/app.ts"}}
	beforeView := comparisonViewForTest(t, srv, p.ID, source, "before")
	afterView := comparisonViewForTest(t, srv, p.ID, source, "after")
	if comparisonTextForTest(t, srv, p.ID, beforeView) != "committed\n" || comparisonTextForTest(t, srv, p.ID, afterView) != "committed change\n" {
		t.Fatal("comparison did not retain the commit endpoints")
	}
	for _, path := range []string{"../outside.txt", "/absolute", "missing.txt"} {
		source.GitChange.Path = path
		rejected := comparisonViewForTest(t, srv, p.ID, source, "after")
		if rejected.State != "failed" {
			t.Fatalf("accepted %q", path)
		}
	}
	other := createProjectForTest(t, srv, t.TempDir())
	if code := requestGitReview(t, srv, other.ID, "change", "review", nil, nil); code != 404 {
		t.Fatalf("foreign project status %d", code)
	}
}

func TestGitAmendReviewSeparatesCommitFromMovement(t *testing.T) {
	repo, root := nestedRootRepo(t)
	_, database, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, root)
	mirrorLedgerProject(t, database, p)
	parent := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	path := filepath.Join(root, "new.txt")
	testutil.FailErr(t, "write first", os.WriteFile(path, []byte("one\n"), 0o644))
	gittest.CommitAll(t, repo, "first")
	before := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	testutil.FailErr(t, "write amended", os.WriteFile(path, []byte("one\ntwo\n"), 0o644))
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "--amend", "-m", "amended")
	after := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	recordGitReviewMovement(t, database, p, "amend", "amend", before, after, 1)
	var page wire.SourceGitReview
	if code := requestGitReview(t, srv, p.ID, "amend", "review", nil, &page); code != 200 {
		t.Fatalf("commit review status %d", code)
	}
	if page.BeforeCommit != parent || page.Insertions != 2 {
		t.Fatalf("commit review = %+v", page)
	}
	if code := requestGitReview(t, srv, p.ID, "amend", "review", url.Values{"movement": {"true"}}, &page); code != 200 {
		t.Fatalf("movement review status %d", code)
	}
	if page.BeforeCommit != before || page.Insertions != 1 || page.CommitComparison {
		t.Fatalf("movement review = %+v", page)
	}
	for _, query := range []url.Values{{"parent": {"2"}}, {"movement": {"true"}, "parent": {"1"}}, {"movement": {"yes"}}, {"cursor": {"1"}}, {"limit": {"0"}}} {
		if code := requestGitReview(t, srv, p.ID, "amend", "review", query, nil); code != 400 {
			t.Fatalf("query %v status %d", query, code)
		}
	}
	recordGitReviewMovement(t, database, p, "missing", "commit", before, strings.Repeat("f", 40), 2)
	if code := requestGitReview(t, srv, p.ID, "missing", "review", nil, nil); code != 422 {
		t.Fatalf("missing object status %d", code)
	}
}

func TestGitReviewSpecialFileComparisons(t *testing.T) {
	repo, root := nestedRootRepo(t)
	_, database, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, root)
	mirrorLedgerProject(t, database, p)
	before := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	testutil.FailErr(t, "write binary", os.WriteFile(filepath.Join(root, "binary"), []byte{0, 1, 2}, 0o644))
	testutil.FailErr(t, "write large blob", os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", project.SourceReadMaxBytes+1)), 0o644))
	testutil.FailErr(t, "write symlink", os.Symlink("../outside-secret", filepath.Join(root, "link")))
	gittest.Run(t, root, "mv", "src/app.ts", "src/renamed.ts")
	gittest.CommitAll(t, repo, "special files")
	after := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	recordGitReviewMovement(t, database, p, "special", "commit", before, after, 1)
	for _, row := range []struct{ path, availability, reason string }{
		{"binary", "binary", "binary_content"}, {"large", "not_captured", "content_too_large"}, {"link", "available", ""}, {"src/renamed.ts", "available", ""},
	} {
		view := comparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{GitChange: &wire.GitChangeComparisonSource{Kind: "git_change", ChangeID: "special", Path: row.path}}, "after")
		diff := view.Comparison
		if view.State != "ready" || diff == nil || diff.After == nil || diff.After.Availability != row.availability || diff.After.Reason != row.reason {
			t.Fatalf("path %s: view=%+v", row.path, view)
		}
		if row.path == "link" && comparisonTextForTest(t, srv, p.ID, view) != "../outside-secret" {
			t.Fatalf("symlink followed: %+v", diff.After)
		}
		if row.path == "src/renamed.ts" && (diff.Before.Path != "src/app.ts" || !diff.LocationChanged || diff.Op != wire.SourceChangeOpRename) {
			t.Fatalf("rename = %+v", diff)
		}
	}
}

func TestGitMergeReviewSelectsEachParent(t *testing.T) {
	repo, root := nestedRootRepo(t)
	_, database, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, root)
	mirrorLedgerProject(t, database, p)
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
	recordGitReviewMovement(t, database, p, "merge", "merge", first, after, 1)
	var page wire.SourceGitReview
	if code := requestGitReview(t, srv, p.ID, "merge", "review", nil, &page); code != 200 {
		t.Fatalf("first parent status %d", code)
	}
	if len(page.Commit.Parents) != 2 || page.BeforeCommit != first || len(page.Files) != 1 || page.Files[0].Path != "topic.txt" {
		t.Fatalf("first parent = %+v", page)
	}
	if code := requestGitReview(t, srv, p.ID, "merge", "review", url.Values{"parent": {"2"}}, &page); code != 200 {
		t.Fatalf("second parent status %d", code)
	}
	if page.BeforeCommit != second || len(page.Files) != 1 || page.Files[0].Path != "main.txt" {
		t.Fatalf("second parent = %+v", page)
	}
}
