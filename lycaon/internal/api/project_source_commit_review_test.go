package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCommitReviewKeepsIndexOnlyAndUnavailableChanges(t *testing.T) {
	_, root := nestedRootRepo(t)
	_, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, root)
	mirrorLedgerProject(t, ledgerDB, p)
	path := filepath.Join(root, "src", "app.ts")
	testutil.FailErr(t, "write staged contents", os.WriteFile(path, []byte("index only\n"), 0o644))
	gittest.Run(t, root, "add", "src/app.ts")
	testutil.FailErr(t, "restore working contents", os.WriteFile(path, []byte("committed\n"), 0o644))
	testutil.FailErr(t, "write binary", os.WriteFile(filepath.Join(root, "binary"), []byte{0, 1, 0}, 0o644))
	outside := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "write outside contents", os.WriteFile(outside, []byte("outside secret"), 0o644))
	testutil.FailErr(t, "link outside root", os.Symlink(outside, filepath.Join(root, "link")))
	page := readCommitReview(t, srv, p.ID, "", "100")
	files := make(map[string]wire.SourceWalkFile)
	for _, file := range page.Files {
		files[file.Path] = file
	}
	if files["src/app.ts"].HeadMatch != wire.SourceHeadMatchSame || files["src/app.ts"].Commit.Status != "MM" {
		t.Fatalf("index-only changes vanished: %+v", files["src/app.ts"])
	}
	if files["binary"].Commit.Availability != "binary" || files["link"].Commit.Availability != "unavailable" {
		t.Fatalf("unavailable paths vanished: %+v", files)
	}
	code, diff := getSourceComparison(t, srv, p.ID, url.Values{"baseline": {"commit"}, "root_id": {p.Roots[0].ID}, "path": {"link"}})
	if code != http.StatusOK || diff.After == nil || diff.After.Availability != "unavailable" || diff.After.Content != "" {
		t.Fatalf("link comparison crossed root: code=%d diff=%+v", code, diff)
	}
}

func readCommitReview(t *testing.T, srv *Server, projectID, cursor string, limit string) wire.SourceWalkResponse {
	t.Helper()
	q := url.Values{"baseline": {"commit"}, "limit": {limit}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, newAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/source/walk?"+q.Encode(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("review status=%d body=%s", w.Code, w.Body.String())
	}
	var response wire.SourceWalkResponse
	testutil.FailErr(t, "decode commit review", json.Unmarshal(w.Body.Bytes(), &response))
	return response
}

func TestCommitReviewIncludesUnobservedGitChangesWithoutCreatingHistory(t *testing.T) {
	repo, nested := nestedRootRepo(t)
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, nested)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	write := func(path string, content []byte) {
		t.Helper()
		testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(nested, path), content, 0o644))
	}
	write("gone.txt", []byte("deleted before the app opened\n"))
	write("renamed.txt", []byte("rename before the app opened\n"))
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-m", "baseline")
	write("src/app.ts", []byte("working\n"))
	testutil.FailErr(t, "delete unseen file", os.Remove(filepath.Join(nested, "gone.txt")))
	gittest.Run(t, nested, "mv", "renamed.txt", "destination.txt")
	testutil.FailErr(t, "create untracked directory", os.Mkdir(filepath.Join(nested, "new"), 0o755))
	write("new/a.txt", []byte("new\n"))
	write("new/b.txt", []byte("also new\n"))
	write("binary.dat", []byte{0, 1, 0, 2})
	write("staged.txt", []byte("index\n"))
	gittest.Run(t, nested, "add", "staged.txt")
	files := make(map[string]wire.SourceWalkFile)
	cursor := ""
	for page := 0; page < 20; page++ {
		response := readCommitReview(t, srv, p.ID, cursor, "2")
		if !response.CommitAvailable || len(response.CommitRoots) != 1 {
			t.Fatalf("roots=%+v", response.CommitRoots)
		}
		for _, file := range response.Files {
			if _, duplicate := files[file.Path]; duplicate {
				t.Fatalf("duplicate %s", file.Path)
			}
			files[file.Path] = file
			if file.FileID != "" || len(file.Effects) != 0 {
				t.Fatalf("unobserved path acquired history: %+v", file)
			}
		}
		if response.NextCursor == "" {
			break
		}
		if response.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = response.NextCursor
	}
	for _, path := range []string{"src/app.ts", "gone.txt", "renamed.txt", "destination.txt", "new/a.txt", "new/b.txt", "binary.dat", "staged.txt"} {
		if _, ok := files[path]; !ok {
			t.Fatalf("missing Git path %q: %+v", path, files)
		}
	}
	if len(files) != 8 {
		t.Fatalf("files=%d want 8", len(files))
	}
	if files["gone.txt"].Tip.State != wire.SourceTipStateAbsent || files["gone.txt"].Commit.Op != wire.SourceChangeOpDelete {
		t.Fatal("deletion lost its comparison")
	}
	if files["binary.dat"].Commit.Availability != "binary" {
		t.Fatalf("binary=%+v", files["binary.dat"])
	}
	if files["staged.txt"].Commit.Status != "A." {
		t.Fatalf("staged=%+v", files["staged.txt"])
	}
	for _, tc := range []struct{ path, before, after string }{
		{"src/app.ts", "committed\n", "working\n"},
		{"gone.txt", "deleted before the app opened\n", ""},
		{"new/a.txt", "", "new\n"},
		{"renamed.txt", "rename before the app opened\n", ""},
		{"destination.txt", "", "rename before the app opened\n"},
	} {
		code, diff := getSourceComparison(t, srv, p.ID, url.Values{"baseline": {"commit"}, "root_id": {rootID}, "path": {tc.path}, "expected_head": {files[tc.path].Commit.Head}})
		if code != 200 || diff.Before == nil || diff.After == nil {
			t.Fatalf("%s comparison=%+v code=%d", tc.path, diff, code)
		}
		if diff.Before.Content != tc.before || diff.After.Content != tc.after {
			t.Fatalf("%s endpoints=%+v / %+v", tc.path, diff.Before, diff.After)
		}
		if diff.Before.VersionID != "" || diff.After.VersionID != "" {
			t.Fatal("live Git comparison claimed retained versions")
		}
	}
	walk, err := ledger.QueryWalk(t.Context(), p.ID, sourceledger.Baseline{Kind: sourceledger.BaselinePresentation}, 100, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "read recorded history", err)
	if len(walk.Files) != 0 {
		t.Fatal("Git review invented observed effects")
	}
	var count int
	testutil.FailErr(t, "count identities", ledgerDB.QueryRowContext(t.Context(), "SELECT count(*) FROM source_files").Scan(&count))
	if count != 0 {
		t.Fatalf("read-only review created %d identities", count)
	}
}

func TestCommitReviewBeforeFirstCommitAndChangedHead(t *testing.T) {
	repo := t.TempDir()
	gittest.Init(t, repo)
	_, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, repo)
	mirrorLedgerProject(t, ledgerDB, p)
	testutil.FailErr(t, "write initial file", os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o644))
	page := readCommitReview(t, srv, p.ID, "", "100")
	if len(page.Files) != 1 || page.Files[0].Commit.Head != "" || !page.CommitAvailable {
		t.Fatalf("unborn=%+v", page)
	}
	q := url.Values{"baseline": {"commit"}, "root_id": {p.Roots[0].ID}, "path": {"new.txt"}, "expected_head": {""}}
	code, diff := getSourceComparison(t, srv, p.ID, q)
	if code != 200 || diff.Before == nil || diff.Before.State != "absent" {
		t.Fatalf("unborn comparison=%+v status=%d", diff, code)
	}
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-m", "first")
	code, _ = getSourceComparison(t, srv, p.ID, q)
	if code != http.StatusConflict {
		t.Fatalf("changed HEAD status=%d", code)
	}
	page = readCommitReview(t, srv, p.ID, "", "100")
	if len(page.Files) != 0 {
		t.Fatalf("committed paths stayed dirty: %+v", page.Files)
	}
}

func TestCommitReviewHistoryEnrichesLiveContentsAndPreservesPresentation(t *testing.T) {
	_, root := nestedRootRepo(t)
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, root)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "record agent change", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, RootID: rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginAgent, Before: []byte("committed\n"), After: []byte("recorded\n"),
	}))
	testutil.FailErr(t, "write unseen successor", os.WriteFile(filepath.Join(root, "src/app.ts"), []byte("live successor\n"), 0o644))
	page := readCommitReview(t, srv, p.ID, "", "100")
	if len(page.Files) != 1 {
		t.Fatalf("files=%+v", page.Files)
	}
	file := page.Files[0]
	if file.FileID == "" || len(file.Effects) != 1 || file.UnpresentedAgentEffects != 1 || !file.ChangedSincePresented || file.PresentationEffectID == "" {
		t.Fatalf("recorded enrichment=%+v", file)
	}
	code, diff := getSourceComparison(t, srv, p.ID, url.Values{
		"baseline": {"commit"}, "root_id": {rootID}, "path": {"src/app.ts"},
	})
	if code != http.StatusOK || diff.After == nil || diff.After.Content != "live successor\n" {
		t.Fatalf("comparison used recorded head: status=%d diff=%+v", code, diff)
	}
	testutil.FailErr(t, "record deletion", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, RootID: rootID, Path: "src/app.ts", Op: wire.SourceChangeOpDelete,
		Origin: wire.SourceChangeOriginUser, Before: []byte("recorded\n"),
	}))
	page = readCommitReview(t, srv, p.ID, "", "100")
	if page.Files[0].FileID != "" || len(page.Files[0].Effects) != 0 {
		t.Fatal("unobserved recreation inherited a deleted identity")
	}
}

func TestCommitReviewRejectsPaginationAcrossMembershipChanges(t *testing.T) {
	_, root := nestedRootRepo(t)
	_, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	p := createProjectForTest(t, srv, root)
	mirrorLedgerProject(t, ledgerDB, p)
	for _, path := range []string{"b.txt", "c.txt"} {
		testutil.FailErr(t, "write untracked", os.WriteFile(filepath.Join(root, path), []byte("new"), 0o644))
	}
	first := readCommitReview(t, srv, p.ID, "", "1")
	if first.NextCursor == "" {
		t.Fatal("missing path cursor")
	}
	testutil.FailErr(t, "insert earlier dirty path", os.WriteFile(filepath.Join(root, "a.txt"), []byte("new"), 0o644))
	q := url.Values{"baseline": {"commit"}, "cursor": {first.NextCursor}}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/walk?"+q.Encode(), nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("membership change status=%d body=%s", w.Code, w.Body.String())
	}
}
