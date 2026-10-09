package hostcontracts

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGitWorktreeBindHappyPath(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, view := contractfixture.CreateBoundSession(t, srv, projectID)
	if !view.Bound || view.State != "ready" || view.Path != "" {
		t.Fatalf("view = %#v", view)
	}
	binding, ok, err := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
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
	workspace := contractfixture.GetSourceWorkspaceView(t, srv, projectID, sess.ID)
	if len(workspace.Roots) != 1 || filepath.Clean(workspace.Roots[0].Path) != filepath.Clean(binding.WorktreePath) {
		t.Fatalf("source workspace roots = %#v, want checkout %q", workspace.Roots, binding.WorktreePath)
	}
	if !workspace.SessionScoped {
		t.Fatal("a chat's worktree workspace must declare that reads carry the chat")
	}
}

func TestSourceWorkspaceDeclaresProjectCheckoutForUnboundChats(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	first, second := contractfixture.CreateSessionForProject(t, srv, projectID), contractfixture.CreateSessionForProject(t, srv, projectID)
	a, b := contractfixture.GetSourceWorkspaceView(t, srv, projectID, first.ID), contractfixture.GetSourceWorkspaceView(t, srv, projectID, second.ID)
	if a.SessionScoped || b.SessionScoped {
		t.Fatalf("unbound chats resolve to the project checkout: %v %v", a.SessionScoped, b.SessionScoped)
	}
	if a.WorkspaceID != b.WorkspaceID {
		t.Fatalf("unbound chats must share one workspace, got %q and %q", a.WorkspaceID, b.WorkspaceID)
	}
}

func TestGitWorktreeBindDerivesBranch(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, view := contractfixture.CreateBoundSession(t, srv, projectID)
	if view.Branch != "session/"+sess.ID {
		t.Fatalf("branch = %q", view.Branch)
	}
}

func TestGitWorktreeBindRejectsTakenBranch(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	p, err := srv.Sources.Workspace.ProjectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	root := project.PrimaryRootPath(p)
	contractfixture.RunGitIn(t, root, "branch", "taken-branch")
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	repoID := contractfixture.WorktreeRepoID(t, srv, projectID)
	bw, _ := contractfixture.BindWorktree(t, srv, sess.ID, repoID, "taken-branch")
	if bw.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", bw.Code, bw.Body.String())
	}
	if contractfixture.DecodeErr(t, bw).Code != "worktree_branch_exists" {
		t.Fatalf("code = %q", contractfixture.DecodeErr(t, bw).Code)
	}
	_, ok, err := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "get", err)
	if ok {
		t.Fatal("no row should be written")
	}
}

func TestGitWorktreeBindRejectsSecondBinding(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, repoID, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	bw, _ := contractfixture.BindWorktree(t, srv, sess.ID, repoID, "session/other")
	if bw.Code != http.StatusConflict || contractfixture.DecodeErr(t, bw).Code != "worktree_already_bound" {
		t.Fatalf("status=%d body=%s", bw.Code, bw.Body.String())
	}
}

func TestGitWorktreeBindRejectsBusySession(t *testing.T) {
	srv, mgr, projectID := contractfixture.NewWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	unlock, ok := mgr.Execution.TryIdleMutation(sess.ID)
	if !ok {
		t.Fatal("hold idle lock")
	}
	defer unlock()
	repoID := contractfixture.WorktreeRepoID(t, srv, projectID)
	bw, _ := contractfixture.BindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID)
	if bw.Code != http.StatusConflict || contractfixture.DecodeErr(t, bw).Code != "session_not_idle" {
		t.Fatalf("status=%d body=%s", bw.Code, bw.Body.String())
	}
}

func TestGitWorktreeBindRejectsUnavailableRepo(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	bw, _ := contractfixture.BindWorktree(t, srv, sess.ID, "missing-repo", "session/"+sess.ID)
	if bw.Code != http.StatusNotFound || contractfixture.DecodeErr(t, bw).Code != "git_repo_not_found" {
		t.Fatalf("status=%d body=%s", bw.Code, bw.Body.String())
	}
}

func TestGitWorktreeBindRejectsDetachedHead(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	p, err := srv.Sources.Workspace.ProjectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	root := project.PrimaryRootPath(p)
	sha := strings.TrimSpace(string(contractfixture.MustGitOutput(t, root, "rev-parse", "HEAD")))
	contractfixture.RunGitIn(t, root, "checkout", "--detach", sha)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	repoID := contractfixture.WorktreeRepoID(t, srv, projectID)
	bw, _ := contractfixture.BindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID)
	resp := contractfixture.DecodeErr(t, bw)
	if bw.Code != http.StatusConflict || resp.Code != "worktree_land_blocked" {
		t.Fatalf("status=%d body=%s", bw.Code, bw.Body.String())
	}
	if resp.Details["reason"] != "detached_head" {
		t.Fatalf("details = %#v", resp.Details)
	}
}

func TestGitWorktreeBindLeavesConfigUntouched(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	p, err := srv.Sources.Workspace.ProjectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "project", err)
	root := project.PrimaryRootPath(p)
	mgr := git.NewManager()
	before, err := mgr.RepoConfigFingerprint(t.Context(), root)
	testutil.FailErr(t, "fingerprint before", err)
	_, _, view := contractfixture.CreateBoundSession(t, srv, projectID)
	after, err := mgr.RepoConfigFingerprint(t.Context(), root)
	testutil.FailErr(t, "fingerprint after", err)
	if before != after {
		t.Fatalf("config fingerprint changed: %s -> %s", before, after)
	}
	binding, ok, err := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), view.SessionID)
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
	dir := contractfixture.InitCommittedRepoDir(t)
	mem := store.NewMemory()
	failing := &contractfixture.PutFailStore{Store: mem, FailPut: true}
	mock := llm.NewMockProvider(contractfixture.TestMockConfig(t))
	mgr := session.NewHost(failing, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	contractfixture.WireTestBindingRegistry(t)
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "open", err)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: failing, Projects: reg, Sessions: mgr, UserNotices: contractfixture.TestUserNotices(t)}, Storage: hostapi.StorageDependencies{DataDir: t.TempDir()}, Workflow: hostapi.WorkflowDependencies{
		Board: contractfixture.NewGitBoard(), RepoSetCache: git.NewRepoSetCache(git.DefaultStatusCacheTTL)}}), nil, hostapi.TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	repoID := contractfixture.WorktreeRepoID(t, srv, p.ID)
	bw, _ := contractfixture.BindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID)
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
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	gw, view := contractfixture.GetWorktree(t, srv, sess.ID)
	if gw.Code != http.StatusOK || view.Bound || view.AheadOfBase != 0 || view.BehindBase != 0 || view.Dirty {
		t.Fatalf("status=%d view=%#v", gw.Code, view)
	}
}

func TestGitWorktreeViewStaleAndReady(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, ok, err := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "binding", err)
	if !ok {
		t.Fatal("expected binding")
	}
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(binding.WorktreePath, "extra.txt"), []byte("x\n"), 0o644))
	contractfixture.RunGitIn(t, binding.WorktreePath, "add", "extra.txt")
	contractfixture.RunGitIn(t, binding.WorktreePath, "commit", "-m", "ahead")
	testutil.FailErr(t, "dirty file", os.WriteFile(filepath.Join(binding.WorktreePath, "dirty.txt"), []byte("d\n"), 0o644))
	_, ready := contractfixture.GetWorktree(t, srv, sess.ID)
	if ready.State != "ready" || ready.AheadOfBase != 1 || !ready.Dirty || ready.Path != "" {
		t.Fatalf("ready view = %#v", ready)
	}
	testutil.FailErr(t, "remove wt", os.RemoveAll(binding.WorktreePath))
	_, stale := contractfixture.GetWorktree(t, srv, sess.ID)
	if stale.State != "stale" || !stale.Bound || stale.AheadOfBase != 0 || stale.Path == "" {
		t.Fatalf("stale view = %#v", stale)
	}
}

func TestGitWorktreeLandHappyAndNothingToLand(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, ok, err := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "binding", err)
	if !ok {
		t.Fatal("expected binding")
	}
	w := contractfixture.LandWorktree(t, srv, sess.ID)
	var land wire.GitWorktreeLandResult
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &land))
	if w.Code != http.StatusOK || land.Landed || land.Reason != "nothing_to_land" {
		t.Fatalf("status=%d land=%#v", w.Code, land)
	}

	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(binding.WorktreePath, "landed.txt"), []byte("y\n"), 0o644))
	contractfixture.RunGitIn(t, binding.WorktreePath, "add", "landed.txt")
	contractfixture.RunGitIn(t, binding.WorktreePath, "commit", "-m", "land me")
	beforeFP, err := git.NewManager().RepoConfigFingerprint(t.Context(), binding.Toplevel)
	testutil.FailErr(t, "fp", err)
	w = contractfixture.LandWorktree(t, srv, sess.ID)
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
	if _, ok, err := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID); err != nil || !ok {
		t.Fatal("binding must remain after land")
	}
}

func TestGitWorktreeLandBaseDirty(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, _, _ := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "write wt", os.WriteFile(filepath.Join(binding.WorktreePath, "a.txt"), []byte("a\n"), 0o644))
	contractfixture.RunGitIn(t, binding.WorktreePath, "add", "a.txt")
	contractfixture.RunGitIn(t, binding.WorktreePath, "commit", "-m", "a")
	testutil.FailErr(t, "dirty base", os.WriteFile(filepath.Join(binding.Toplevel, "base-dirty.txt"), []byte("d\n"), 0o644))
	w := contractfixture.LandWorktree(t, srv, sess.ID)
	resp := contractfixture.DecodeErr(t, w)
	if w.Code != http.StatusConflict || resp.Code != "worktree_land_blocked" || resp.Details["reason"] != "base_dirty" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandBaseOnOtherBranch(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, _, _ := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "write wt", os.WriteFile(filepath.Join(binding.WorktreePath, "a.txt"), []byte("a\n"), 0o644))
	contractfixture.RunGitIn(t, binding.WorktreePath, "add", "a.txt")
	contractfixture.RunGitIn(t, binding.WorktreePath, "commit", "-m", "a")
	contractfixture.RunGitIn(t, binding.Toplevel, "checkout", "-b", "other")
	w := contractfixture.LandWorktree(t, srv, sess.ID)
	resp := contractfixture.DecodeErr(t, w)
	if w.Code != http.StatusConflict || resp.Details["reason"] != "base_on_other_branch" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandBaseBranchDeleted(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, _, _ := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "write wt", os.WriteFile(filepath.Join(binding.WorktreePath, "a.txt"), []byte("a\n"), 0o644))
	contractfixture.RunGitIn(t, binding.WorktreePath, "add", "a.txt")
	contractfixture.RunGitIn(t, binding.WorktreePath, "commit", "-m", "a")
	contractfixture.RunGitIn(t, binding.Toplevel, "checkout", "-b", "other")
	contractfixture.RunGitIn(t, binding.Toplevel, "branch", "-D", binding.BaseBranch)
	w := contractfixture.LandWorktree(t, srv, sess.ID)
	resp := contractfixture.DecodeErr(t, w)
	if w.Code != http.StatusConflict || resp.Details["reason"] != "base_branch_missing" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandConflict(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, _, _ := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "wt edit", os.WriteFile(filepath.Join(binding.WorktreePath, "README.md"), []byte("wt\n"), 0o644))
	contractfixture.RunGitIn(t, binding.WorktreePath, "add", "README.md")
	contractfixture.RunGitIn(t, binding.WorktreePath, "commit", "-m", "wt")
	testutil.FailErr(t, "base edit", os.WriteFile(filepath.Join(binding.Toplevel, "README.md"), []byte("base\n"), 0o644))
	contractfixture.RunGitIn(t, binding.Toplevel, "add", "README.md")
	contractfixture.RunGitIn(t, binding.Toplevel, "commit", "-m", "base")
	w := contractfixture.LandWorktree(t, srv, sess.ID)
	resp := contractfixture.DecodeErr(t, w)
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
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, _, _ := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "dirty", os.WriteFile(filepath.Join(binding.WorktreePath, "d.txt"), []byte("d\n"), 0o644))
	w := contractfixture.LandWorktree(t, srv, sess.ID)
	if w.Code != http.StatusConflict || contractfixture.DecodeErr(t, w).Code != "worktree_dirty" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandNotBound(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	lw := contractfixture.LandWorktree(t, srv, sess.ID)
	if lw.Code != http.StatusConflict || contractfixture.DecodeErr(t, lw).Code != "worktree_not_bound" {
		t.Fatalf("status=%d body=%s", lw.Code, lw.Body.String())
	}
}

func TestGitWorktreeRemoveNotBound(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var sess wire.Session
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &sess))
	rw := contractfixture.RemoveWorktree(t, srv, sess.ID)
	if rw.Code != http.StatusConflict || contractfixture.DecodeErr(t, rw).Code != "worktree_not_bound" {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
}

func TestGitWorktreeLandBusy(t *testing.T) {
	srv, mgr, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	unlock, ok := mgr.TryIdleMutation(sess.ID)
	if !ok {
		t.Fatal("hold lock")
	}
	defer unlock()
	w := contractfixture.LandWorktree(t, srv, sess.ID)
	if w.Code != http.StatusConflict || contractfixture.DecodeErr(t, w).Code != "session_not_idle" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeLandStale(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, _, _ := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "remove", os.RemoveAll(binding.WorktreePath))
	w := contractfixture.LandWorktree(t, srv, sess.ID)
	if w.Code != http.StatusConflict || contractfixture.DecodeErr(t, w).Code != "worktree_stale" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeRemoveHappyDirtyStale(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	binding, _, _ := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	branch := binding.Branch
	path := binding.WorktreePath

	testutil.FailErr(t, "dirty", os.WriteFile(filepath.Join(path, "d.txt"), []byte("d\n"), 0o644))
	w := contractfixture.RemoveWorktree(t, srv, sess.ID)
	if w.Code != http.StatusConflict || contractfixture.DecodeErr(t, w).Code != "worktree_dirty" {
		t.Fatalf("dirty status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("directory must remain when dirty")
	}
	testutil.FailErr(t, "clean dirty", os.Remove(filepath.Join(path, "d.txt")))

	beforeFP, err := git.NewManager().RepoConfigFingerprint(t.Context(), binding.Toplevel)
	testutil.FailErr(t, "fp", err)
	w = contractfixture.RemoveWorktree(t, srv, sess.ID)
	if w.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", w.Code, w.Body.String())
	}
	if _, view := contractfixture.GetWorktree(t, srv, sess.ID); view.Bound {
		t.Fatalf("view after remove = %#v", view)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory should be gone: %v", err)
	}
	_, ok, err := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
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
	repoID := contractfixture.WorktreeRepoID(t, srv, projectID)
	bw, _ := contractfixture.BindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID+"-2")
	if bw.Code != http.StatusOK {
		t.Fatalf("rebind status=%d body=%s", bw.Code, bw.Body.String())
	}
	binding2, _, _ := srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "rm stale", os.RemoveAll(binding2.WorktreePath))
	w = contractfixture.RemoveWorktree(t, srv, sess.ID)
	if w.Code != http.StatusNoContent {
		t.Fatalf("stale remove status=%d body=%s", w.Code, w.Body.String())
	}
	_, ok, err = srv.Sources.Workspace.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "get after stale", err)
	if ok {
		t.Fatal("stale row should be gone")
	}
	entries, err := git.NewManager().ListWorktrees(t.Context(), binding2.Toplevel)
	testutil.FailErr(t, "list after stale repair", err)
	for _, entry := range entries {
		if contractfixture.SameReachPath(entry.Path, binding2.WorktreePath) {
			t.Fatalf("stale git administration entry remains: %#v", entry)
		}
	}
}

func TestDeleteSessionMappedWhenWorktreeBound(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	sess, _, _ := contractfixture.CreateBoundSession(t, srv, projectID)
	req := contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || contractfixture.DecodeErr(t, w).Code != "worktree_already_bound" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGitWorktreeEndpointCount(t *testing.T) {
	root := filepath.Dir(configlayout.FindModuleRoot())
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
