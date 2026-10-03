//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type putFailStore struct {
	session.Store
	failPut bool
}

func (s *putFailStore) PutWorktreeBinding(ctx context.Context, b store.WorktreeBinding) error {
	if s.failPut {
		return errors.New("injected put failure")
	}
	return s.Store.PutWorktreeBinding(ctx, b)
}

func newWorktreeTestServer(t *testing.T, opts ...testDeps) (*Server, *session.Manager, string) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := initCommittedRepoDir(t)
	// Sessions and projects share the database the source ledger records in.
	database := testdbfixture.Open(t, "worktree.db")
	mem := store.NewSQL(database)
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewManager(mem, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	wireTestBindingRegistry(t)
	reg := project.NewSQLRegistry(database)
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open", err)
	mgr.SetProjectRegistry(reg)
	srv := newServerForTest(t, Dependencies{
		Database: database, Store: mem, Projects: reg, Sessions: mgr, DataDir: t.TempDir(),
		Board: newGitBoard(), RepoSetCache: git.NewRepoSetCache(git.DefaultStatusCacheTTL),
	}, opts...)
	return srv, mgr, p.ID
}

func initCommittedRepoDir(t *testing.T) string {
	t.Helper()
	dir, _ := initCommittedRepo(t)
	return dir
}

func worktreeRepoID(t *testing.T, srv *Server, projectID string) string {
	t.Helper()
	return activeRepoID(t, srv, projectID)
}

func worktreeURL(sessionID string) string {
	return "/v1/sessions/" + sessionID + "/git-worktree"
}

// sendWorktreeJSON serves one request; a nil body sends none.
func sendWorktreeJSON(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		testutil.FailErr(t, "marshal", err)
		reader = bytes.NewReader(raw)
	}
	req := newAuthedRequest(method, path, reader)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func postWorktreeJSON(t *testing.T, srv *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return sendWorktreeJSON(t, srv, http.MethodPost, path, body)
}

func landWorktree(t *testing.T, srv *Server, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	return sendWorktreeJSON(t, srv, http.MethodPost, worktreeURL(sessionID)+"/land", nil)
}

func removeWorktree(t *testing.T, srv *Server, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	return sendWorktreeJSON(t, srv, http.MethodDelete, worktreeURL(sessionID), nil)
}

func getWorktree(t *testing.T, srv *Server, sessionID string) (*httptest.ResponseRecorder, wire.GitWorktreeView) {
	t.Helper()
	req := newAuthedRequest(http.MethodGet, worktreeURL(sessionID), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var view wire.GitWorktreeView
	if w.Code == http.StatusOK {
		testutil.FailErr(t, "decode view", json.Unmarshal(w.Body.Bytes(), &view))
	}
	return w, view
}

func getSourceWorkspaceView(t *testing.T, srv *Server, projectID, sessionID string) wire.SourceWorkspace {
	t.Helper()
	url := "/v1/projects/" + projectID + "/source/workspace?session_id=" + sessionID
	req := newAuthedRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("source workspace status = %d body=%s", w.Code, w.Body.String())
	}
	var view wire.SourceWorkspace
	testutil.FailErr(t, "decode source workspace", json.Unmarshal(w.Body.Bytes(), &view))
	return view
}

func bindWorktree(t *testing.T, srv *Server, sessionID, repoID, branch string) (*httptest.ResponseRecorder, wire.GitWorktreeView) {
	t.Helper()
	body := wire.GitWorktreeBindRequest{RepoID: repoID, Branch: branch}
	w := sendWorktreeJSON(t, srv, http.MethodPut, worktreeURL(sessionID), body)
	var view wire.GitWorktreeView
	if w.Code == http.StatusOK {
		testutil.FailErr(t, "decode bind", json.Unmarshal(w.Body.Bytes(), &view))
	}
	return w, view
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) wire.ErrorResponse {
	t.Helper()
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode error", json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// createSessionForProject creates one chat and drains the work its creation queues.
func createSessionForProject(t *testing.T, srv *Server, projectID string) wire.Session {
	t.Helper()
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body=%s", w.Code, w.Body.String())
	}
	var sess wire.Session
	testutil.FailErr(t, "decode session", json.Unmarshal(w.Body.Bytes(), &sess))
	drainBackground(t, srv)
	return sess
}

func createBoundSession(t *testing.T, srv *Server, projectID string) (wire.Session, string, wire.GitWorktreeView) {
	t.Helper()
	sess := createSessionForProject(t, srv, projectID)
	repoID := worktreeRepoID(t, srv, projectID)
	bw, view := bindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID)
	if bw.Code != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", bw.Code, bw.Body.String())
	}
	return sess, repoID, view
}

func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	gittest.Run(t, dir, args...)
}

func TestGitWorktreeBindHappyPath(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, view := createBoundSession(t, srv, projectID)
	if !view.Bound || view.State != "ready" || view.Path != "" {
		t.Fatalf("view = %#v", view)
	}
	binding, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "get binding", err)
	if !ok {
		t.Fatal("expected binding row")
	}
	if _, err := os.Stat(filepath.Join(binding.WorktreePath, ".git")); err != nil {
		t.Fatalf("worktree missing: %v", err)
	}
	branch, err := git.NewManager().Branch(t.Context(), binding.WorktreePath)
	testutil.FailErr(t, "branch", err)
	if branch != binding.Branch || branch != "session/"+sess.ID {
		t.Fatalf("branch = %q want session/%s", branch, sess.ID)
	}
	workspace := getSourceWorkspaceView(t, srv, projectID, sess.ID)
	if len(workspace.Roots) != 1 || filepath.Clean(workspace.Roots[0].Path) != filepath.Clean(binding.WorktreePath) {
		t.Fatalf("source workspace roots = %#v, want checkout %q", workspace.Roots, binding.WorktreePath)
	}
	if !workspace.SessionScoped {
		t.Fatal("a chat's worktree workspace must declare that reads carry the chat")
	}
}

func TestSourceWorkspaceDeclaresProjectCheckoutForUnboundChats(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	first, second := createSessionForProject(t, srv, projectID), createSessionForProject(t, srv, projectID)
	a, b := getSourceWorkspaceView(t, srv, projectID, first.ID), getSourceWorkspaceView(t, srv, projectID, second.ID)
	if a.SessionScoped || b.SessionScoped {
		t.Fatalf("unbound chats resolve to the project checkout: %v %v", a.SessionScoped, b.SessionScoped)
	}
	if a.WorkspaceID != b.WorkspaceID {
		t.Fatalf("unbound chats must share one workspace, got %q and %q", a.WorkspaceID, b.WorkspaceID)
	}
}

func TestGitWorktreeBindDerivesBranch(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, view := createBoundSession(t, srv, projectID)
	if view.Branch != "session/"+sess.ID {
		t.Fatalf("branch = %q", view.Branch)
	}
}

func TestGitWorktreeBindRejectsTakenBranch(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	p, err := srv.projectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	root := project.PrimaryRootPath(p)
	runGitIn(t, root, "branch", "taken-branch")
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	repoID := worktreeRepoID(t, srv, projectID)
	bw, _ := bindWorktree(t, srv, sess.ID, repoID, "taken-branch")
	if bw.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", bw.Code, bw.Body.String())
	}
	if decodeErr(t, bw).Code != "worktree_branch_exists" {
		t.Fatalf("code = %q", decodeErr(t, bw).Code)
	}
	_, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "get", err)
	if ok {
		t.Fatal("no row should be written")
	}
}

func TestGitWorktreeBindRejectsSecondBinding(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, repoID, _ := createBoundSession(t, srv, projectID)
	bw, _ := bindWorktree(t, srv, sess.ID, repoID, "session/other")
	if bw.Code != http.StatusConflict || decodeErr(t, bw).Code != "worktree_already_bound" {
		t.Fatalf("status=%d body=%s", bw.Code, bw.Body.String())
	}
}

func TestGitWorktreeBindRejectsBusySession(t *testing.T) {
	srv, mgr, projectID := newWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	unlock, ok := mgr.TryIdleMutation(sess.ID)
	if !ok {
		t.Fatal("hold idle lock")
	}
	defer unlock()
	repoID := worktreeRepoID(t, srv, projectID)
	bw, _ := bindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID)
	if bw.Code != http.StatusConflict || decodeErr(t, bw).Code != "session_not_idle" {
		t.Fatalf("status=%d body=%s", bw.Code, bw.Body.String())
	}
}

func TestGitWorktreeBindRejectsUnavailableRepo(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	bw, _ := bindWorktree(t, srv, sess.ID, "missing-repo", "session/"+sess.ID)
	if bw.Code != http.StatusNotFound || decodeErr(t, bw).Code != "git_repo_not_found" {
		t.Fatalf("status=%d body=%s", bw.Code, bw.Body.String())
	}
}

func TestGitWorktreeBindRejectsDetachedHead(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	p, err := srv.projectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	root := project.PrimaryRootPath(p)
	sha := strings.TrimSpace(string(mustGitOutput(t, root, "rev-parse", "HEAD")))
	runGitIn(t, root, "checkout", "--detach", sha)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	repoID := worktreeRepoID(t, srv, projectID)
	bw, _ := bindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID)
	resp := decodeErr(t, bw)
	if bw.Code != http.StatusConflict || resp.Code != "worktree_land_blocked" {
		t.Fatalf("status=%d body=%s", bw.Code, bw.Body.String())
	}
	if resp.Details["reason"] != "detached_head" {
		t.Fatalf("details = %#v", resp.Details)
	}
}

func mustGitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	return []byte(gittest.Run(t, dir, args...))
}

func TestGitWorktreeBindLeavesConfigUntouched(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	p, err := srv.projectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	root := project.PrimaryRootPath(p)
	mgr := git.NewManager()
	before, err := mgr.RepoConfigFingerprint(t.Context(), root)
	testutil.FailErr(t, "fingerprint before", err)
	_, _, view := createBoundSession(t, srv, projectID)
	after, err := mgr.RepoConfigFingerprint(t.Context(), root)
	testutil.FailErr(t, "fingerprint after", err)
	if before != after {
		t.Fatalf("config fingerprint changed: %s -> %s", before, after)
	}
	binding, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), view.SessionID)
	testutil.FailErr(t, "binding", err)
	if !ok {
		t.Fatal("expected binding")
	}
	fromWT, err := mgr.RepoConfigFingerprint(t.Context(), binding.WorktreePath)
	testutil.FailErr(t, "fingerprint worktree", err)
	if fromWT != before {
		t.Fatalf("linked worktree fingerprint diverged")
	}
}

func TestGitWorktreeBindRollsBack(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := initCommittedRepoDir(t)
	mem := store.NewMemory()
	failing := &putFailStore{Store: mem, failPut: true}
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewManager(failing, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	wireTestBindingRegistry(t)
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "open", err)
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: failing, Projects: reg, Sessions: mgr, DataDir: t.TempDir(), UserNotices: testUserNotices(t),
		Board: newGitBoard(), RepoSetCache: git.NewRepoSetCache(git.DefaultStatusCacheTTL),
	}), nil, TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	repoID := worktreeRepoID(t, srv, p.ID)
	bw, _ := bindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID)
	if bw.Code < 500 {
		t.Fatalf("expected store failure status, got %d body=%s", bw.Code, bw.Body.String())
	}
	entries, err := git.NewManager().ListWorktrees(t.Context(), dir)
	testutil.FailErr(t, "list", err)
	for _, e := range entries {
		if strings.Contains(e.Path, sess.ID) {
			t.Fatalf("orphan worktree left behind: %#v", e)
		}
	}
}

func TestGitWorktreeViewUnbound(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	gw, view := getWorktree(t, srv, sess.ID)
	if gw.Code != http.StatusOK || view.Bound || view.AheadOfBase != 0 || view.BehindBase != 0 || view.Dirty {
		t.Fatalf("status=%d view=%#v", gw.Code, view)
	}
}

func TestGitWorktreeViewStaleAndReady(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "binding", err)
	if !ok {
		t.Fatal("expected binding")
	}
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(binding.WorktreePath, "extra.txt"), []byte("x\n"), 0o644))
	runGitIn(t, binding.WorktreePath, "add", "extra.txt")
	runGitIn(t, binding.WorktreePath, "commit", "-m", "ahead")
	testutil.FailErr(t, "dirty file", os.WriteFile(filepath.Join(binding.WorktreePath, "dirty.txt"), []byte("d\n"), 0o644))
	_, ready := getWorktree(t, srv, sess.ID)
	if ready.State != "ready" || ready.AheadOfBase != 1 || !ready.Dirty || ready.Path != "" {
		t.Fatalf("ready view = %#v", ready)
	}
	testutil.FailErr(t, "remove wt", os.RemoveAll(binding.WorktreePath))
	_, stale := getWorktree(t, srv, sess.ID)
	if stale.State != "stale" || !stale.Bound || stale.AheadOfBase != 0 || stale.Path == "" {
		t.Fatalf("stale view = %#v", stale)
	}
}

func TestGitWorktreeLandHappyAndNothingToLand(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "binding", err)
	if !ok {
		t.Fatal("expected binding")
	}
	w := landWorktree(t, srv, sess.ID)
	var land wire.GitWorktreeLandResult
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &land))
	if w.Code != http.StatusOK || land.Landed || land.Reason != "nothing_to_land" {
		t.Fatalf("status=%d land=%#v", w.Code, land)
	}

	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(binding.WorktreePath, "landed.txt"), []byte("y\n"), 0o644))
	runGitIn(t, binding.WorktreePath, "add", "landed.txt")
	runGitIn(t, binding.WorktreePath, "commit", "-m", "land me")
	beforeFP, err := git.NewManager().RepoConfigFingerprint(t.Context(), binding.Toplevel)
	testutil.FailErr(t, "fp", err)
	w = landWorktree(t, srv, sess.ID)
	testutil.FailErr(t, "decode land", json.Unmarshal(w.Body.Bytes(), &land))
	if w.Code != http.StatusOK || !land.Landed || land.Commits != 1 {
		t.Fatalf("status=%d land=%#v body=%s", w.Code, land, w.Body.String())
	}
	afterFP, err := git.NewManager().RepoConfigFingerprint(t.Context(), binding.Toplevel)
	testutil.FailErr(t, "fp after", err)
	if beforeFP != afterFP {
		t.Fatal("config changed on land")
	}
	if _, err := os.Stat(filepath.Join(binding.Toplevel, "landed.txt")); err != nil {
		t.Fatalf("merge did not land file: %v", err)
	}
	if _, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID); err != nil || !ok {
		t.Fatal("binding must remain after land")
	}
}

func TestGitWorktreeLandBaseDirty(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, _, _ := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "write wt", os.WriteFile(filepath.Join(binding.WorktreePath, "a.txt"), []byte("a\n"), 0o644))
	runGitIn(t, binding.WorktreePath, "add", "a.txt")
	runGitIn(t, binding.WorktreePath, "commit", "-m", "a")
	testutil.FailErr(t, "dirty base", os.WriteFile(filepath.Join(binding.Toplevel, "base-dirty.txt"), []byte("d\n"), 0o644))
	w := landWorktree(t, srv, sess.ID)
	resp := decodeErr(t, w)
	if w.Code != http.StatusConflict || resp.Code != "worktree_land_blocked" || resp.Details["reason"] != "base_dirty" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandBaseOnOtherBranch(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, _, _ := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "write wt", os.WriteFile(filepath.Join(binding.WorktreePath, "a.txt"), []byte("a\n"), 0o644))
	runGitIn(t, binding.WorktreePath, "add", "a.txt")
	runGitIn(t, binding.WorktreePath, "commit", "-m", "a")
	runGitIn(t, binding.Toplevel, "checkout", "-b", "other")
	w := landWorktree(t, srv, sess.ID)
	resp := decodeErr(t, w)
	if w.Code != http.StatusConflict || resp.Details["reason"] != "base_on_other_branch" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandBaseBranchDeleted(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, _, _ := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "write wt", os.WriteFile(filepath.Join(binding.WorktreePath, "a.txt"), []byte("a\n"), 0o644))
	runGitIn(t, binding.WorktreePath, "add", "a.txt")
	runGitIn(t, binding.WorktreePath, "commit", "-m", "a")
	runGitIn(t, binding.Toplevel, "checkout", "-b", "other")
	runGitIn(t, binding.Toplevel, "branch", "-D", binding.BaseBranch)
	w := landWorktree(t, srv, sess.ID)
	resp := decodeErr(t, w)
	if w.Code != http.StatusConflict || resp.Details["reason"] != "base_branch_missing" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandConflict(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, _, _ := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "wt edit", os.WriteFile(filepath.Join(binding.WorktreePath, "README.md"), []byte("wt\n"), 0o644))
	runGitIn(t, binding.WorktreePath, "add", "README.md")
	runGitIn(t, binding.WorktreePath, "commit", "-m", "wt")
	testutil.FailErr(t, "base edit", os.WriteFile(filepath.Join(binding.Toplevel, "README.md"), []byte("base\n"), 0o644))
	runGitIn(t, binding.Toplevel, "add", "README.md")
	runGitIn(t, binding.Toplevel, "commit", "-m", "base")
	w := landWorktree(t, srv, sess.ID)
	resp := decodeErr(t, w)
	if w.Code != http.StatusConflict || resp.Code != "worktree_land_blocked" || resp.Details["reason"] != "conflict" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	conflicts, _ := resp.Details["conflicts"].([]any)
	found := false
	for _, c := range conflicts {
		if s, ok := c.(string); ok && strings.Contains(s, "README.md") {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflicts = %#v", resp.Details["conflicts"])
	}
	if _, err := os.Stat(filepath.Join(binding.Toplevel, ".git", "MERGE_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MERGE_HEAD should be absent: %v", err)
	}
	st, err := git.NewManager().Status(t.Context(), binding.Toplevel)
	testutil.FailErr(t, "status", err)
	if st.Dirty {
		t.Fatal("base tree must be clean after abort")
	}
}

func TestGitWorktreeLandWorktreeDirty(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, _, _ := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "dirty", os.WriteFile(filepath.Join(binding.WorktreePath, "d.txt"), []byte("d\n"), 0o644))
	w := landWorktree(t, srv, sess.ID)
	if w.Code != http.StatusConflict || decodeErr(t, w).Code != "worktree_dirty" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandNotBound(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	lw := landWorktree(t, srv, sess.ID)
	if lw.Code != http.StatusConflict || decodeErr(t, lw).Code != "worktree_not_bound" {
		t.Fatalf("status=%d body=%s", lw.Code, lw.Body.String())
	}
}

func TestGitWorktreeRemoveNotBound(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	rw := removeWorktree(t, srv, sess.ID)
	if rw.Code != http.StatusConflict || decodeErr(t, rw).Code != "worktree_not_bound" {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
}

func TestGitWorktreeLandBusy(t *testing.T) {
	srv, mgr, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	unlock, ok := mgr.TryIdleMutation(sess.ID)
	if !ok {
		t.Fatal("hold lock")
	}
	defer unlock()
	w := landWorktree(t, srv, sess.ID)
	if w.Code != http.StatusConflict || decodeErr(t, w).Code != "session_not_idle" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandStale(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, _, _ := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "remove", os.RemoveAll(binding.WorktreePath))
	w := landWorktree(t, srv, sess.ID)
	if w.Code != http.StatusConflict || decodeErr(t, w).Code != "worktree_stale" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeRemoveHappyDirtyStale(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	binding, _, _ := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	branch := binding.Branch
	path := binding.WorktreePath

	testutil.FailErr(t, "dirty", os.WriteFile(filepath.Join(path, "d.txt"), []byte("d\n"), 0o644))
	w := removeWorktree(t, srv, sess.ID)
	if w.Code != http.StatusConflict || decodeErr(t, w).Code != "worktree_dirty" {
		t.Fatalf("dirty status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("directory must remain when dirty")
	}
	testutil.FailErr(t, "clean dirty", os.Remove(filepath.Join(path, "d.txt")))

	beforeFP, err := git.NewManager().RepoConfigFingerprint(t.Context(), binding.Toplevel)
	testutil.FailErr(t, "fp", err)
	w = removeWorktree(t, srv, sess.ID)
	if w.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", w.Code, w.Body.String())
	}
	if _, view := getWorktree(t, srv, sess.ID); view.Bound {
		t.Fatalf("view after remove = %#v", view)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory should be gone: %v", err)
	}
	_, ok, err := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "get", err)
	if ok {
		t.Fatal("row should be gone")
	}
	afterFP, err := git.NewManager().RepoConfigFingerprint(t.Context(), binding.Toplevel)
	testutil.FailErr(t, "fp after", err)
	if beforeFP != afterFP {
		t.Fatal("config changed on remove")
	}
	if _, err := git.NewManager().RevParse(t.Context(), binding.Toplevel, []string{"refs/heads/" + branch}); err != nil {
		t.Fatalf("branch must remain: %v", err)
	}

	// A missing checkout still releases its binding.
	repoID := worktreeRepoID(t, srv, projectID)
	bw, _ := bindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID+"-2")
	if bw.Code != http.StatusOK {
		t.Fatalf("rebind status=%d body=%s", bw.Code, bw.Body.String())
	}
	binding2, _, _ := srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "rm stale", os.RemoveAll(binding2.WorktreePath))
	w = removeWorktree(t, srv, sess.ID)
	if w.Code != http.StatusNoContent {
		t.Fatalf("stale remove status=%d body=%s", w.Code, w.Body.String())
	}
	_, ok, err = srv.sessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "get after stale", err)
	if ok {
		t.Fatal("stale row should be gone")
	}
	entries, err := git.NewManager().ListWorktrees(t.Context(), binding2.Toplevel)
	testutil.FailErr(t, "list after stale repair", err)
	for _, entry := range entries {
		if sameReachPath(entry.Path, binding2.WorktreePath) {
			t.Fatalf("stale git administration entry remains: %#v", entry)
		}
	}
}

func TestDeleteSessionMappedWhenWorktreeBound(t *testing.T) {
	srv, _, projectID := newWorktreeTestServer(t)
	sess, _, _ := createBoundSession(t, srv, projectID)
	req := newAuthedRequest(http.MethodDelete, "/v1/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || decodeErr(t, w).Code != "worktree_already_bound" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeEndpointCount(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	testutil.FailErr(t, "read openapi", err)
	var paths []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "/v1/") || !strings.HasSuffix(line, ":") {
			continue
		}
		if strings.Contains(line, "/git/repos") || strings.Contains(line, "/git-worktree") {
			paths = append(paths, strings.TrimSuffix(line, ":"))
		}
	}
	if len(paths) != 13 {
		t.Fatalf("git paths = %d want 13 (11 repository + 2 worktree): %v", len(paths), paths)
	}
}
