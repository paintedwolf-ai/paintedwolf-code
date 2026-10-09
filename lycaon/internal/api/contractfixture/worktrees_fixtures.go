package contractfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
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

func BindWorktree(t *testing.T, srv *hostapi.Server, sessionID, repoID, branch string) (*httptest.ResponseRecorder, wire.GitWorktreeView) {
	t.Helper()
	body := wire.GitWorktreeBindRequest{RepoID: repoID, Branch: branch}
	w := SendWorktreeJSON(t, srv, http.MethodPut, WorktreeURL(sessionID), body)
	var view wire.GitWorktreeView
	if w.Code == http.StatusOK {
		testutil.FailErr(t, "decode bind", json.Unmarshal(w.Body.Bytes(), &view))
	}
	return w, view
}

func CreateBoundSession(t *testing.T, srv *hostapi.Server, projectID string) (wire.Session, string, wire.GitWorktreeView) {
	t.Helper()
	sess := CreateSessionForProject(t, srv, projectID)
	repoID := WorktreeRepoID(t, srv, projectID)
	bw, view := BindWorktree(t, srv, sess.ID, repoID, "session/"+sess.ID)
	if bw.Code != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", bw.Code, bw.Body.String())
	}
	return sess, repoID, view
}

func CreateSessionForProject(t *testing.T, srv *hostapi.Server, projectID string) wire.Session {
	t.Helper()
	body := `{"project_id":"` + projectID + `","posture":"build"}`
	req := NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body=%s", w.Code, w.Body.String())
	}
	var sess wire.Session
	testutil.FailErr(t, "decode session", json.Unmarshal(w.Body.Bytes(), &sess))
	DrainBackground(t, srv)
	return sess
}

func DecodeErr(t *testing.T, w *httptest.ResponseRecorder) wire.ErrorResponse {
	t.Helper()
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode error", json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// createSessionForProject creates one chat and drains the work its creation queues.

func GetSourceWorkspaceView(t *testing.T, srv *hostapi.Server, projectID, sessionID string) wire.SourceWorkspace {
	t.Helper()
	url := "/v1/projects/" + projectID + "/source/workspace?session_id=" + sessionID
	req := NewAuthedRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("source workspace status = %d body=%s", w.Code, w.Body.String())
	}
	var view wire.SourceWorkspace
	testutil.FailErr(t, "decode source workspace", json.Unmarshal(w.Body.Bytes(), &view))
	return view
}

func GetWorktree(t *testing.T, srv *hostapi.Server, sessionID string) (*httptest.ResponseRecorder, wire.GitWorktreeView) {
	t.Helper()
	req := NewAuthedRequest(http.MethodGet, WorktreeURL(sessionID), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var view wire.GitWorktreeView
	if w.Code == http.StatusOK {
		testutil.FailErr(t, "decode view", json.Unmarshal(w.Body.Bytes(), &view))
	}
	return w, view
}

func InitCommittedRepoDir(t *testing.T) string {
	t.Helper()
	dir, _ := InitCommittedRepo(t)
	return dir
}

func LandWorktree(t *testing.T, srv *hostapi.Server, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	return SendWorktreeJSON(t, srv, http.MethodPost, WorktreeURL(sessionID)+"/land", nil)
}

func MustGitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	return []byte(gittest.Run(t, dir, args...))
}

func NewWorktreeTestServer(t *testing.T, opts ...TestDeps) (*hostapi.Server, *session.Manager, string) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := InitCommittedRepoDir(t)
	// Sessions and projects share the database the source ledger records in.
	database := testdbfixture.Open(t, "worktree.db")
	mem := store.NewSQL(database)
	mock := llm.NewMockProvider(TestMockConfig(t))
	mgr := session.NewManager(mem, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	WireTestBindingRegistry(t)
	reg := project.NewSQLRegistry(database)
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open", err)
	mgr.SetProjectRegistry(reg)
	srv := NewServerForTest(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Database: database, Store: mem, Projects: reg, Sessions: mgr}, Storage: hostapi.StorageDependencies{DataDir: t.TempDir()}, Workflow: hostapi.WorkflowDependencies{
		Board: NewGitBoard(), RepoSetCache: git.NewRepoSetCache(git.DefaultStatusCacheTTL)}}, opts...)
	return srv, mgr, p.ID
}

func PostWorktreeJSON(t *testing.T, srv *hostapi.Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return SendWorktreeJSON(t, srv, http.MethodPost, path, body)
}

type PutFailStore struct {
	session.Store
	FailPut bool
}

func (s *PutFailStore) PutWorktreeBinding(ctx context.Context, b store.WorktreeBinding) error {
	if s.FailPut {
		return errors.New("injected put failure")
	}
	return s.Store.PutWorktreeBinding(ctx, b)
}

func RemoveWorktree(t *testing.T, srv *hostapi.Server, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	return SendWorktreeJSON(t, srv, http.MethodDelete, WorktreeURL(sessionID), nil)
}

func RunGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	gittest.Run(t, dir, args...)
}

func SameReachPath(a, b string) bool {
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

func SendWorktreeJSON(t *testing.T, srv *hostapi.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		testutil.FailErr(t, "marshal", err)
		reader = bytes.NewReader(raw)
	}
	req := NewAuthedRequest(method, path, reader)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func WorktreeRepoID(t *testing.T, srv *hostapi.Server, projectID string) string {
	t.Helper()
	return ActiveRepoID(t, srv, projectID)
}

func WorktreeURL(sessionID string) string {
	return "/v1/sessions/" + sessionID + "/git-worktree"
}

// sendWorktreeJSON serves one request; a nil body sends none.
