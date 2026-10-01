//go:build integration

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/project"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWorktreeReach_realProductWorkflow(t *testing.T) {
	srv, mgr, projectID := newWorktreeTestServer(t)
	mgr.SetProjectRegistry(srv.projectRegistry)
	sess, baseRepoID, _ := createBoundSession(t, srv, projectID)
	binding, bound, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "get binding", err)
	if !bound {
		t.Fatal("expected binding")
	}

	w := postWorktreeJSON(t, srv, gitRepoURL(projectID, baseRepoID, "checkout")+"?session_id="+sess.ID, wire.GitCheckoutRequest{
		Branch: binding.BaseBranch,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bound checkout status = %d body=%s", w.Code, w.Body.String())
	}

	sourceURL := "/v1/projects/" + projectID + "/source?path=README.md&session_id=" + sess.ID
	req := newAuthedRequest(http.MethodGet, sourceURL, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("source read status = %d body=%s", w.Code, w.Body.String())
	}
	var source wire.ProjectSourceReadResponse
	testutil.FailErr(t, "decode source", json.Unmarshal(w.Body.Bytes(), &source))

	writeBody, err := json.Marshal(wire.PutProjectSourceRequest{
		OperationID: uuid.NewString(),
		Path:        "README.md",
		RootID:      source.RootID,
		Content:     "changed in session\n",
		Encoding:    source.Encoding,
		BaseSHA256:  source.SHA256,
	})
	testutil.FailErr(t, "marshal source write", err)
	req = newAuthedRequest(http.MethodPut, "/v1/projects/"+projectID+"/source?session_id="+sess.ID, bytes.NewReader(writeBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("source write status = %d body=%s", w.Code, w.Body.String())
	}

	p, err := srv.projectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "get project", err)
	baseREADME := filepath.Join(project.PrimaryRootPath(p), "README.md")
	baseContent, err := os.ReadFile(baseREADME)
	testutil.FailErr(t, "read base before land", err)
	if string(baseContent) != "hello\n" {
		t.Fatalf("base changed before land: %q", baseContent)
	}

	repos := gitReposViaAPI(t, srv, projectID, sess.ID)
	if len(repos.Repos) == 0 || repos.Repos[0].RepoID != baseRepoID {
		t.Fatalf("bound repo identity = %#v want %q", repos.Repos, baseRepoID)
	}
	status := gitStatusViaAPI(t, srv, projectID, baseRepoID, sess.ID)
	if !status.Dirty || status.Branch != binding.Branch {
		t.Fatalf("bound status = %#v", status)
	}

	w = postWorktreeJSON(t, srv, gitRepoURL(projectID, baseRepoID, "commit")+"?session_id="+sess.ID, wire.GitCommitRequest{
		Message: "feat: session change",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("bound commit = %d body=%s", w.Code, w.Body.String())
	}
	status = gitStatusViaAPI(t, srv, projectID, baseRepoID, sess.ID)
	if status.Dirty || status.Branch != binding.Branch {
		t.Fatalf("commit status = %#v", status)
	}

	w = landWorktree(t, srv, sess.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("land = %d body=%s", w.Code, w.Body.String())
	}
	var land wire.GitWorktreeLandResult
	testutil.FailErr(t, "decode land", json.Unmarshal(w.Body.Bytes(), &land))
	if !land.Landed || land.Commits != 1 {
		t.Fatalf("land result = %#v", land)
	}
	baseContent, err = os.ReadFile(baseREADME)
	testutil.FailErr(t, "read base after land", err)
	if string(baseContent) != "changed in session\n" {
		t.Fatalf("base after land = %q", baseContent)
	}

	w = removeWorktree(t, srv, sess.ID)
	if w.Code != http.StatusNoContent {
		t.Fatalf("unbind = %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(binding.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree remains after unbind: %v", err)
	}
}

func sameReachPath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return filepath.Clean(ra) == filepath.Clean(rb)
}

func TestWorktreeReach_tabAndBoardAgree(t *testing.T) {
	gm := git.NewManager()
	repoProvider := repotest.NewProvider(t)
	t.Cleanup(func() { _ = repoProvider.Close() })
	srv, mgr, projectID := newWorktreeTestServer(t, func(d *Dependencies) {
		d.Board = &board.SnapshotBuilder{
			Git:      gm,
			Repo:     repoProvider,
			Worktree: d.Sessions.BoardGitWorktreeFunc(gm),
		}
	})
	mgr.SetProjectRegistry(srv.projectRegistry)

	sess, _, view := createBoundSession(t, srv, projectID)
	binding, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "binding", err)
	if !ok {
		t.Fatal("expected binding")
	}

	p, err := srv.projectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	roots := project.RootRefsFrom(p)
	repos, err := srv.Git.LoadOrderedRepos(t.Context(), p, roots, sess.WorkspaceRootID, sess.ID)
	testutil.FailErr(t, "loadOrderedRepos", err)
	if len(repos) == 0 || !repos[0].Available {
		t.Fatalf("repos = %#v", repos)
	}
	if !sameReachPath(repos[0].Toplevel, binding.WorktreePath) {
		t.Fatalf("tab toplevel = %q want %q", repos[0].Toplevel, binding.WorktreePath)
	}
	tabBranch, err := gm.Branch(t.Context(), repos[0].Toplevel)
	testutil.FailErr(t, "tab branch", err)

	agentRoots := project.SubstituteWorktreeRoots(roots, project.Binding{
		Toplevel: binding.Toplevel, WorktreePath: binding.WorktreePath,
		Branch: binding.Branch, BaseBranch: binding.BaseBranch,
	})
	// The board reads Git status through the status cache, which fills after
	// its first revalidation.
	var snap *wire.BoardSnapshot
	testutil.WaitFor(t, 10*time.Second, func() bool {
		built, err := srv.board.Build(t.Context(), projectID, binding.WorktreePath, sess.ID, wire.BoardDetailLevelCompact, agentRoots)
		testutil.FailErr(t, "board Build", err)
		snap = built
		return snap.Git != nil && snap.Git.Worktree != nil
	})
	if snap.Git.Branch != tabBranch || snap.Git.Worktree.Branch != tabBranch {
		t.Fatalf("board branch=%q worktree=%q tab=%q bind=%q", snap.Git.Branch, snap.Git.Worktree.Branch, tabBranch, view.Branch)
	}
	if !sameReachPath(binding.WorktreePath, repos[0].Toplevel) {
		t.Fatalf("agent checkout %q != tab toplevel %q", binding.WorktreePath, repos[0].Toplevel)
	}

	lines := packboard.BuildOrientationLines(*snap, packboard.OrientOpts{})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Worktree: "+tabBranch+" from ") {
		t.Fatalf("board text missing worktree line: %s", joined)
	}
}

func TestWorktreeReach_tabWithoutSessionUnsubstituted(t *testing.T) {
	srv, mgr, projectID := newWorktreeTestServer(t)
	mgr.SetProjectRegistry(srv.projectRegistry)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "binding", err)
	if !ok {
		t.Fatal("expected binding")
	}

	p, err := srv.projectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	roots := project.RootRefsFrom(p)
	withSession, err := srv.Git.LoadOrderedRepos(t.Context(), p, roots, "", sess.ID)
	testutil.FailErr(t, "with session", err)
	without, err := srv.Git.LoadOrderedRepos(t.Context(), p, roots, "", "")
	testutil.FailErr(t, "without session", err)
	if len(without) == 0 || !sameReachPath(without[0].Toplevel, binding.Toplevel) {
		t.Fatalf("session-less toplevel = %#v want %q", without, binding.Toplevel)
	}
	if !sameReachPath(withSession[0].Toplevel, binding.WorktreePath) {
		t.Fatalf("session toplevel = %q want worktree", withSession[0].Toplevel)
	}

	view := gitReposViaAPI(t, srv, projectID, "")
	if len(view.Repos) == 0 {
		t.Fatal("expected repos")
	}
}

func TestWorktreeReach_stalePromptHTTP409(t *testing.T) {
	srv, mgr, projectID := newWorktreeTestServer(t)
	mgr.SetProjectRegistry(srv.projectRegistry)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "binding", err)
	if !ok {
		t.Fatal("expected binding")
	}
	testutil.FailErr(t, "remove worktree", os.RemoveAll(binding.WorktreePath))

	body := promptJSON("hello")
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &resp))
	if resp.Code != "worktree_stale" {
		t.Fatalf("code = %q", resp.Code)
	}

	req = newAuthedRequest(http.MethodGet, gitReposURL(projectID)+"?session_id="+sess.ID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("repos status = %d body=%s", w.Code, w.Body.String())
	}
	testutil.FailErr(t, "decode repos err", json.Unmarshal(w.Body.Bytes(), &resp))
	if resp.Code != "worktree_stale" {
		t.Fatalf("repos code = %q", resp.Code)
	}
}

func TestWorktreeReach_unknownSessionNeverFallsBackToBaseCheckout(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	req := newAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/source?path=README.md&session_id=missing-session", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestWorktreeReach_boardLineAbsentWhenUnbound(t *testing.T) {
	gm := git.NewManager()
	repoProvider := repotest.NewProvider(t)
	t.Cleanup(func() { _ = repoProvider.Close() })
	srv, mgr, projectID := newWorktreeTestServer(t, func(d *Dependencies) {
		d.Board = &board.SnapshotBuilder{Git: gm, Repo: repoProvider, Worktree: d.Sessions.BoardGitWorktreeFunc(gm)}
	})
	mgr.SetProjectRegistry(srv.projectRegistry)

	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))

	p, err := srv.projectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	root := project.PrimaryRootPath(p)
	snap, err := srv.board.Build(t.Context(), projectID, root, sess.ID, wire.BoardDetailLevelCompact, project.RootRefsFrom(p))
	testutil.FailErr(t, "Build", err)
	if snap.Git != nil && snap.Git.Worktree != nil {
		t.Fatalf("unbound worktree fact = %#v", snap.Git.Worktree)
	}
	lines := packboard.BuildOrientationLines(*snap, packboard.OrientOpts{})
	for _, line := range lines {
		if strings.HasPrefix(line, "Worktree:") {
			t.Fatalf("unexpected worktree line: %q", line)
		}
	}
}

func TestWorktreeReachSourceHistoryCannotChangeGitRepositoryIdentity(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	p, err := srv.projectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "load project", err)
	scope, err := requestscope.ResolveSessionProject(srv.sessionStore, t.Context(), p, sess.ID)
	testutil.FailErr(t, "resolve worktree", err)
	if scope.Binding == nil {
		t.Fatal("expected worktree binding")
	}
	// Source history discovers scoped roots before the Git panel hydrates.
	srv.Git.RepoSetCache.Invalidate(projectID)
	_ = srv.Sources.CommitLens(t.Context(), scope.Project)
	for _, sessionID := range []string{sess.ID, "", sess.ID} {
		repos, err := srv.Git.LoadOrderedRepos(t.Context(), p, project.RootRefsFrom(p), sess.WorkspaceRootID, sessionID)
		testutil.FailErr(t, "resolve Git repositories", err)
		wantPath := scope.Binding.Toplevel
		if sessionID != "" {
			wantPath = scope.Binding.WorktreePath
		}
		if len(repos) != 1 || repos[0].ID != scope.Binding.RepoID || len(repos[0].RootIDs) != 1 || repos[0].Label == "" || !sameReachPath(repos[0].Toplevel, wantPath) {
			t.Fatalf("session %q repository identity changed after source history: %+v", sessionID, repos)
		}
	}
}
