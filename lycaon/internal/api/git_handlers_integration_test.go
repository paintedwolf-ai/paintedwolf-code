//go:build integration

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// newGitTestServer returns a server with a real git Manager and RepoSetCache wired,
// and a registry holding one committed, dirty repo.
func newGitTestServer(t *testing.T, opts ...testDeps) (*Server, string) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := initDirtyRepo(t)
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open", err)

	drafter := func(d *Dependencies) { d.CommitDrafter = compaction.MockSummarizer{Text: "feat: drafted subject"} }
	srv := newGitServer(t, reg, append([]testDeps{drafter}, opts...)...)
	return srv, p.ID
}

// newGitServer serves reg over a memory session store with a real git board
// and repo-set cache; opts adjust the dependencies after those defaults.
func newGitServer(t *testing.T, reg project.Registry, opts ...testDeps) *Server {
	t.Helper()
	deps := Dependencies{
		Store: store.NewMemory(), Projects: reg,
		Board: newGitBoard(), RepoSetCache: git.NewRepoSetCache(git.DefaultStatusCacheTTL),
	}
	for _, opt := range opts {
		opt(&deps)
	}
	return NewServer(requiredTestDeps(t, deps), nil, TestAPIToken)
}

func initCommittedRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		gittest.Run(t, dir, args...)
	}
	gittest.Init(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	return dir, run
}

func initDirtyRepo(t *testing.T) string {
	t.Helper()
	dir, _ := initCommittedRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	return dir
}

func newGitBoard() *board.SnapshotBuilder {
	manager := git.NewManager()
	return &board.SnapshotBuilder{Git: manager, StatusCache: git.NewStatusCache(manager)}
}

func activeRepoID(t *testing.T, srv *Server, projectID string) string {
	t.Helper()
	view := gitReposViaAPI(t, srv, projectID, "")
	if view.ActiveRepoID == "" {
		t.Fatalf("expected active_repo_id, got %#v", view)
	}
	return view.ActiveRepoID
}

func gitReposViaAPI(t *testing.T, srv *Server, projectID, sessionID string) wire.GitReposView {
	t.Helper()
	url := gitReposURL(projectID)
	if sessionID != "" {
		url += "?session_id=" + sessionID
	}
	req := newAuthedRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("git repos = %d body = %s", w.Code, w.Body.String())
	}
	var view wire.GitReposView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode repos: %v", err)
	}
	return view
}

// gitStatusSnapshot joins one status generation with its changed files.
type gitStatusSnapshot struct {
	wire.GitStatusSummary
	Files []wire.GitFileEntry
}

func gitStatusViaAPI(t *testing.T, srv *Server, projectID, repoID string, sessionIDs ...string) gitStatusSnapshot {
	t.Helper()
	sessionID := ""
	if len(sessionIDs) > 0 {
		sessionID = sessionIDs[0]
	}
	statusURL := gitRepoURL(projectID, repoID, "status")
	lens := ""
	if sessionID != "" {
		statusURL += "?session_id=" + sessionID
		lens = "&session_id=" + sessionID
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := newAuthedRequest(http.MethodGet, statusURL, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
			t.Fatalf("git status = %d body = %s", w.Code, w.Body.String())
		}
		var summary wire.GitStatusSummary
		if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if w.Code == http.StatusAccepted {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if summary.Refreshing {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		changesReq := newAuthedRequest(http.MethodGet,
			fmt.Sprintf("%s?revision=%d&limit=500%s", gitRepoURL(projectID, repoID, "changes"), summary.Revision, lens), nil)
		changesW := httptest.NewRecorder()
		srv.ServeHTTP(changesW, changesReq)
		if changesW.Code == http.StatusAccepted {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if changesW.Code != http.StatusOK {
			t.Fatalf("git changes = %d body = %s", changesW.Code, changesW.Body.String())
		}
		var changes wire.GitChangesPage
		if err := json.Unmarshal(changesW.Body.Bytes(), &changes); err != nil {
			t.Fatalf("decode changes: %v", err)
		}
		if changes.Revision != summary.Revision {
			continue
		}
		return gitStatusSnapshot{GitStatusSummary: summary, Files: changes.Files}
	}
	t.Fatal("git status did not become ready")
	return gitStatusSnapshot{}
}

func gitReposURL(projectID string) string {
	return "/v1/projects/" + projectID + "/git/repos"
}

func gitRepoURL(projectID, repoID, action string) string {
	return gitReposURL(projectID) + "/" + repoID + "/" + action
}

func postGitJSON(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newAuthedRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestGitStatusReportsDirtyFiles(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)
	view := gitStatusViaAPI(t, srv, projectID, repoID)
	if !view.Available {
		t.Fatalf("expected available repo")
	}
	if view.RepoID != repoID || len(view.RootIDs) == 0 {
		t.Fatalf("expected scoped status, got %#v", view)
	}
	if !view.Dirty {
		t.Fatalf("expected dirty repo, got %#v", view)
	}
	paths := map[string]bool{}
	for _, f := range view.Files {
		paths[f.Path] = true
		if f.RootID == "" || f.RootRelativePath == "" {
			t.Fatalf("expected file attribution, got %#v", f)
		}
	}
	if !paths["README.md"] || !paths["new.txt"] {
		t.Fatalf("expected README.md and new.txt in files, got %#v", view.Files)
	}
}

func TestGitCommitClearsTree(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)
	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "commit"), `{"message":"chore: test commit"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("git commit = %d body = %s", w.Code, w.Body.String())
	}
	view := gitStatusViaAPI(t, srv, projectID, repoID)
	if view.Dirty {
		t.Fatalf("expected clean tree after commit, got %#v", view.Files)
	}
	if view.HeadShort == "" {
		t.Fatalf("expected head_short after commit")
	}
}

func TestGitCommitRequiresMessage(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)
	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "commit"), `{"message":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body = %s", w.Code, w.Body.String())
	}
}

func TestGitStashClearsTree(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)
	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "stash"), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("git stash = %d body = %s", w.Code, w.Body.String())
	}
	if view := gitStatusViaAPI(t, srv, projectID, repoID); view.Dirty {
		t.Fatalf("expected clean tree after stash, got %#v", view.Files)
	}
}

func TestGitCommitMessageDraftsFromDiff(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)
	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "commit-message"), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("git commit-message = %d body = %s", w.Code, w.Body.String())
	}
	var resp wire.GitCommitMessageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if strings.TrimSpace(resp.Message) == "" {
		t.Fatalf("expected non-empty drafted message")
	}
}

func TestGitCommitMessageDraftsFromUntrackedOnly(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir, _ := initCommittedRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("brand new file\n"), 0o644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}

	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open", err)
	srv := newGitServer(t, reg, func(d *Dependencies) {
		d.CommitDrafter = compaction.MockSummarizer{Text: "feat: add new file"}
	})

	repoID := activeRepoID(t, srv, p.ID)
	w := postGitJSON(t, srv, gitRepoURL(p.ID, repoID, "commit-message"), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("git commit-message (untracked only) = %d body = %s", w.Code, w.Body.String())
	}
}

func TestGitCommitMessageReportsDraftFailure(t *testing.T) {
	srv, projectID := newGitTestServer(t, func(d *Dependencies) {
		d.CommitDrafter = compaction.MockSummarizer{Err: errors.New("lite model unreachable")}
	})
	repoID := activeRepoID(t, srv, projectID)
	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "commit-message"), `{}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("git commit-message (model failure) = %d body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Code != "git_draft_failed" {
		t.Fatalf("expected git_draft_failed, got %q", resp.Code)
	}
}

func TestGitBranchesAndCheckout(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)

	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "checkout"), `{"branch":"feat/x","create":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("git checkout create = %d body = %s", w.Code, w.Body.String())
	}
	view := gitStatusViaAPI(t, srv, projectID, repoID)
	if view.Branch != "feat/x" {
		t.Fatalf("expected branch feat/x after create, got %q", view.Branch)
	}

	req := newAuthedRequest(http.MethodGet, gitRepoURL(projectID, repoID, "branches"), nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("git branches = %d body = %s", w.Code, w.Body.String())
	}
	var branches wire.GitBranchesView
	if err := json.Unmarshal(w.Body.Bytes(), &branches); err != nil {
		t.Fatalf("decode branches: %v", err)
	}
	var sawCurrent bool
	for _, b := range branches.Branches {
		if b.Name == "feat/x" && b.Current {
			sawCurrent = true
		}
	}
	if !sawCurrent {
		t.Fatalf("expected feat/x flagged current, got %#v", branches.Branches)
	}
}

func TestGitCheckoutRequiresBranch(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)
	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "checkout"), `{"branch":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body = %s", w.Code, w.Body.String())
	}
}

func TestGitInitMakesRepoAvailable(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "reg.Open", err)
	srv := newGitServer(t, reg)

	repos := gitReposViaAPI(t, srv, p.ID, "")
	if len(repos.Repos) != 1 || repos.Repos[0].Available {
		t.Fatalf("expected one non-repo entry, got %#v", repos)
	}
	rootID := repos.Repos[0].RootIDs[0]

	w := postGitJSON(t, srv, gitReposURL(p.ID), fmt.Sprintf(`{"root_id":%q}`, rootID))
	if w.Code != http.StatusCreated {
		t.Fatalf("git init = %d body = %s", w.Code, w.Body.String())
	}
	var ack wire.GitRepoEntry
	if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil {
		t.Fatalf("decode init response: %v", err)
	}
	view := gitStatusViaAPI(t, srv, p.ID, ack.RepoID)
	if !view.Available || view.RepoID == "" {
		t.Fatalf("expected available repo after init, got %#v", view)
	}
	after := gitReposViaAPI(t, srv, p.ID, "")
	if after.ActiveRepoID == "" || !after.Repos[0].Available {
		t.Fatalf("repos list after init should include the new repository: %#v", after)
	}
}

func TestGitDiscardClearsTree(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)
	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "discard"), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("git discard = %d body = %s", w.Code, w.Body.String())
	}
	view := gitStatusViaAPI(t, srv, projectID, repoID)
	if view.Dirty {
		t.Fatalf("expected clean tree after discard, got %#v", view.Files)
	}
}

func TestGitPushWithoutUpstreamFails(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	repoID := activeRepoID(t, srv, projectID)
	w := postGitJSON(t, srv, gitRepoURL(projectID, repoID, "push"), `{}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for push without upstream, got %d body = %s", w.Code, w.Body.String())
	}
}

func TestGitStatusStaleRepoID(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	req := newAuthedRequest(http.MethodGet, gitRepoURL(projectID, "rstale0000000001", "status"), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body = %s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "git_repo_not_found" || resp.Message != gitadmin.GitRepoNotFoundMsg {
		t.Fatalf("unexpected error: %#v", resp)
	}
}

func TestGitInitUnknownRootID(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	w := postGitJSON(t, srv, gitReposURL(projectID), `{"root_id":"root-does-not-exist"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body = %s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "root_not_found" {
		t.Fatalf("unexpected error: %#v", resp)
	}
}

func TestGitReposTwoCheckouts(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	a := initDirtyRepo(t)
	b := initDirtyRepo(t)
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, a)
	testutil.FailErr(t, "create", err)
	_, err = reg.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: b, Label: "beta"})
	testutil.FailErr(t, "attach", err)
	p, err = reg.Get(t.Context(), p.ID)
	testutil.FailErr(t, "get", err)

	srv := newGitServer(t, reg)

	view := gitReposViaAPI(t, srv, p.ID, "")
	if len(view.Repos) != 2 {
		t.Fatalf("want 2 repos, got %#v", view)
	}
	ids := map[string]bool{}
	for _, r := range view.Repos {
		if !r.Available || r.RepoID == "" {
			t.Fatalf("entry: %#v", r)
		}
		ids[r.RepoID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("want distinct ids, got %#v", view)
	}
	if view.ActiveRepoID == "" {
		t.Fatalf("expected active_repo_id")
	}
}

func TestGitReposTwoRootsOneCheckout(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	repo := initDirtyRepo(t)
	web := filepath.Join(repo, "packages", "web")
	testutil.FailErr(t, "mkdir", os.MkdirAll(web, 0o755))
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, repo)
	testutil.FailErr(t, "create", err)
	_, err = reg.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: web, Label: "web"})
	testutil.FailErr(t, "attach", err)

	srv := newGitServer(t, reg)

	view := gitReposViaAPI(t, srv, p.ID, "")
	if len(view.Repos) != 1 {
		t.Fatalf("want 1 repo, got %#v", view)
	}
	if len(view.Repos[0].RootIDs) != 2 {
		t.Fatalf("want both root ids, got %#v", view.Repos[0])
	}
}

// An aged snapshot refreshes behind its values; only a repository without one is pending.
func TestGitReposAgedStatusStaysSettledWhileRefreshing(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	manager := git.NewManager()
	srv, projectID := newGitTestServer(t, func(d *Dependencies) {
		d.Board = &board.SnapshotBuilder{Git: manager, StatusCache: git.NewStatusCache(manager, git.WithStatusCacheClock(clock))}
	})

	var settled wire.GitRepoEntry
	deadline := time.Now().Add(5 * time.Second)
	for {
		view := gitReposViaAPI(t, srv, projectID, "")
		if len(view.Repos) != 1 {
			t.Fatalf("want 1 repo, got %#v", view)
		}
		if settled = view.Repos[0]; !settled.StatusPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first status snapshot did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if settled.Branch == "" || !settled.Dirty || settled.ChangedCount != 2 {
		t.Fatalf("settled summary: %#v", settled)
	}

	clockMu.Lock()
	now = now.Add(2 * git.DefaultStatusCacheTTL)
	clockMu.Unlock()
	for range 3 {
		aged := gitReposViaAPI(t, srv, projectID, "").Repos[0]
		if aged.StatusPending || aged.Branch != settled.Branch || aged.ChangedCount != settled.ChangedCount {
			t.Fatalf("aged summary regressed to pending: %#v", aged)
		}
	}
}

// An aged generation answers its own status and changes; only an absent snapshot defers.
func TestGitStatusAndChangesAnswerFromAnAgedGeneration(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Now()
	manager := git.NewManager()
	srv, projectID := newGitTestServer(t, func(d *Dependencies) {
		d.Board = &board.SnapshotBuilder{Git: manager, StatusCache: git.NewStatusCache(manager, git.WithStatusCacheClock(func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		}))}
	})

	repoID := activeRepoID(t, srv, projectID)
	settled := gitStatusViaAPI(t, srv, projectID, repoID)
	if len(settled.Files) != 2 {
		t.Fatalf("settled files: %#v", settled.Files)
	}

	clockMu.Lock()
	now = now.Add(2 * git.DefaultStatusCacheTTL)
	clockMu.Unlock()
	request := newAuthedRequest(http.MethodGet, gitRepoURL(projectID, repoID, "status"), nil)
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("aged status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var summary wire.GitStatusSummary
	testutil.FailErr(t, "decode aged status", json.Unmarshal(recorder.Body.Bytes(), &summary))
	if !summary.Refreshing || summary.Revision == 0 || summary.Branch != settled.Branch || !summary.Dirty {
		t.Fatalf("aged status: %#v", summary)
	}

	changesRequest := newAuthedRequest(http.MethodGet,
		fmt.Sprintf("%s?revision=%d&limit=500", gitRepoURL(projectID, repoID, "changes"), summary.Revision), nil)
	changesRecorder := httptest.NewRecorder()
	srv.ServeHTTP(changesRecorder, changesRequest)
	if changesRecorder.Code != http.StatusOK {
		t.Fatalf("aged changes = %d body = %s", changesRecorder.Code, changesRecorder.Body.String())
	}
	var changes wire.GitChangesPage
	testutil.FailErr(t, "decode aged changes", json.Unmarshal(changesRecorder.Body.Bytes(), &changes))
	if len(changes.Files) != len(settled.Files) || changes.Revision != summary.Revision {
		t.Fatalf("aged changes: %#v", changes)
	}
}

func TestGitScopedReadsActOnNamedRepo(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	aDir, aRun := initCommittedRepo(t)
	bDir, bRun := initCommittedRepo(t)
	aRun("checkout", "-b", "branch-a")
	bRun("checkout", "-b", "branch-b")
	if err := os.WriteFile(filepath.Join(aDir, "a-only.txt"), []byte("a\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(bDir, "b-only.txt"), []byte("b\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, aDir)
	testutil.FailErr(t, "create", err)
	_, err = reg.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: bDir, Label: "beta"})
	testutil.FailErr(t, "attach", err)

	srv := newGitServer(t, reg)

	repos := gitReposViaAPI(t, srv, p.ID, "")
	if len(repos.Repos) != 2 {
		t.Fatalf("want 2, got %#v", repos)
	}
	var repoA, repoB string
	for _, r := range repos.Repos {
		st := gitStatusViaAPI(t, srv, p.ID, r.RepoID)
		if st.Branch == "branch-a" {
			repoA = r.RepoID
			paths := map[string]bool{}
			for _, f := range st.Files {
				paths[f.Path] = true
			}
			if !paths["a-only.txt"] || paths["b-only.txt"] {
				t.Fatalf("repo A files: %#v", st.Files)
			}
		}
		if st.Branch == "branch-b" {
			repoB = r.RepoID
		}
	}
	if repoA == "" || repoB == "" {
		t.Fatalf("missing scoped repos: A=%q B=%q view=%#v", repoA, repoB, repos)
	}
	stB := gitStatusViaAPI(t, srv, p.ID, repoB)
	if stB.Branch != "branch-b" {
		t.Fatalf("status for B returned %q", stB.Branch)
	}
}

func TestGitScopedWriteLeavesSiblingUntouched(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	aDir := initDirtyRepo(t)
	bDir := initDirtyRepo(t)
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, aDir)
	testutil.FailErr(t, "create", err)
	_, err = reg.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: bDir, Label: "beta"})
	testutil.FailErr(t, "attach", err)

	srv := newGitServer(t, reg)

	repos := gitReposViaAPI(t, srv, p.ID, "")
	if len(repos.Repos) != 2 {
		t.Fatalf("want 2 repos: %#v", repos)
	}
	repoA, repoB := repos.Repos[0].RepoID, repos.Repos[1].RepoID
	beforeB := gitStatusViaAPI(t, srv, p.ID, repoB)

	w := postGitJSON(t, srv, gitRepoURL(p.ID, repoA, "commit"), `{"message":"chore: only A"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("commit A = %d %s", w.Code, w.Body.String())
	}
	afterA := gitStatusViaAPI(t, srv, p.ID, repoA)
	afterB := gitStatusViaAPI(t, srv, p.ID, repoB)
	if afterA.Dirty {
		t.Fatalf("A should be clean: %#v", afterA)
	}
	if afterB.Dirty != beforeB.Dirty || len(afterB.Files) != len(beforeB.Files) {
		t.Fatalf("B changed after A commit: before=%#v after=%#v", beforeB, afterB)
	}
}

func TestGitInitSecondaryRoot(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	primary := initDirtyRepo(t)
	secondary := t.TempDir()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, primary)
	testutil.FailErr(t, "create", err)
	attached, err := reg.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: secondary, Label: "secondary"})
	testutil.FailErr(t, "attach", err)
	var secondaryRootID string
	for _, r := range attached.After.Roots {
		if !r.IsPrimary {
			secondaryRootID = r.ID
		}
	}
	if secondaryRootID == "" {
		t.Fatal("missing secondary root")
	}

	srv := newGitServer(t, reg)

	w := postGitJSON(t, srv, gitReposURL(p.ID), fmt.Sprintf(`{"root_id":%q}`, secondaryRootID))
	if w.Code != http.StatusCreated {
		t.Fatalf("init = %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(secondary, ".git")); err != nil {
		t.Fatalf("secondary .git missing: %v", err)
	}
	// Primary still has its own .git; secondary was empty before.
	repos := gitReposViaAPI(t, srv, p.ID, "")
	if len(repos.Repos) != 2 {
		t.Fatalf("want 2 repos after secondary init: %#v", repos)
	}
}

func TestGitReposUnknownSessionIsRejected(t *testing.T) {
	srv, projectID := newGitTestServer(t)
	req := newAuthedRequest(http.MethodGet, gitReposURL(projectID)+"?session_id=sess-does-not-exist", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("git repos = %d body = %s", w.Code, w.Body.String())
	}
}

func TestGitFileAttributionLongestNestedRoot(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	repo, run := initCommittedRepo(t)
	web := filepath.Join(repo, "packages", "web")
	testutil.FailErr(t, "mkdir web", os.MkdirAll(web, 0o755))
	testutil.FailErr(t, "write web", os.WriteFile(filepath.Join(web, "app.ts"), []byte("web\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "web")
	testutil.FailErr(t, "dirty web", os.WriteFile(filepath.Join(web, "app.ts"), []byte("web2\n"), 0o644))

	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, repo)
	testutil.FailErr(t, "create", err)
	_, err = reg.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: web, Label: "web"})
	testutil.FailErr(t, "attach", err)
	p, err = reg.Get(t.Context(), p.ID)
	testutil.FailErr(t, "get", err)

	var webRootID string
	for _, r := range p.Roots {
		if r.Label == "web" || strings.HasSuffix(filepath.Clean(r.Path), string(filepath.Separator)+"web") {
			webRootID = r.ID
			break
		}
	}
	if webRootID == "" {
		t.Fatalf("missing web root: %#v", p.Roots)
	}

	srv := newGitServer(t, reg)
	view := gitStatusViaAPI(t, srv, p.ID, activeRepoID(t, srv, p.ID))
	var found wire.GitFileEntry
	for _, f := range view.Files {
		if f.Path == "packages/web/app.ts" {
			found = f
		}
	}
	if found.RootID != webRootID || found.RootRelativePath != "app.ts" {
		t.Fatalf("longest root attribution: %#v want %s", found, webRootID)
	}
}

func TestGitFileAttributionLongestRootAndOutside(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	repo, run := initCommittedRepo(t)
	web := filepath.Join(repo, "packages", "web")
	apiDir := filepath.Join(repo, "packages", "api")
	outside := filepath.Join(repo, "other", "pkg")
	testutil.FailErr(t, "mkdir web", os.MkdirAll(web, 0o755))
	testutil.FailErr(t, "mkdir api", os.MkdirAll(apiDir, 0o755))
	testutil.FailErr(t, "mkdir outside", os.MkdirAll(outside, 0o755))
	testutil.FailErr(t, "write web", os.WriteFile(filepath.Join(web, "app.ts"), []byte("web\n"), 0o644))
	testutil.FailErr(t, "write api", os.WriteFile(filepath.Join(apiDir, "main.go"), []byte("api\n"), 0o644))
	testutil.FailErr(t, "write outside", os.WriteFile(filepath.Join(outside, "x.go"), []byte("x\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "files")
	testutil.FailErr(t, "dirty web", os.WriteFile(filepath.Join(web, "app.ts"), []byte("web2\n"), 0o644))
	testutil.FailErr(t, "dirty api", os.WriteFile(filepath.Join(apiDir, "main.go"), []byte("api2\n"), 0o644))
	testutil.FailErr(t, "dirty outside", os.WriteFile(filepath.Join(outside, "x.go"), []byte("x2\n"), 0o644))

	reg := project.NewMemoryRegistry()
	// Roots at packages/web and packages/api only — other/pkg is outside every root.
	p, err := project.CreateWithRoot(t.Context(), reg, web)
	testutil.FailErr(t, "create web root", err)
	_, err = reg.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: apiDir, Label: "api"})
	testutil.FailErr(t, "attach api", err)
	p, err = reg.Get(t.Context(), p.ID)
	testutil.FailErr(t, "get", err)

	var webRootID, apiRootID string
	for _, r := range p.Roots {
		switch {
		case r.Label == "web" || strings.HasSuffix(filepath.Clean(r.Path), string(filepath.Separator)+"web"):
			webRootID = r.ID
		case r.Label == "api" || strings.HasSuffix(filepath.Clean(r.Path), string(filepath.Separator)+"api"):
			apiRootID = r.ID
		}
	}
	if webRootID == "" || apiRootID == "" {
		t.Fatalf("roots: %#v", p.Roots)
	}

	srv := newGitServer(t, reg)
	repoID := activeRepoID(t, srv, p.ID)
	view := gitStatusViaAPI(t, srv, p.ID, repoID)

	byPath := map[string]wire.GitFileEntry{}
	for _, f := range view.Files {
		byPath[f.Path] = f
	}
	webFile := byPath["packages/web/app.ts"]
	if webFile.RootID != webRootID || webFile.RootRelativePath != "app.ts" {
		t.Fatalf("web file attribution: %#v want root %s", webFile, webRootID)
	}
	apiFile := byPath["packages/api/main.go"]
	if apiFile.RootID != apiRootID || apiFile.RootRelativePath != "main.go" {
		t.Fatalf("api file attribution: %#v want root %s", apiFile, apiRootID)
	}
	outFile := byPath["other/pkg/x.go"]
	if outFile.RootID != "" || outFile.RootRelativePath != "" {
		t.Fatalf("outside file should be unlinked: %#v", outFile)
	}
	if outFile.Path == "" {
		t.Fatalf("outside file still listed: %#v", view.Files)
	}
}
