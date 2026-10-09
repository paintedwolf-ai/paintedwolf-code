package contractfixture

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func ActiveRepoID(t *testing.T, srv *hostapi.Server, projectID string) string {
	t.Helper()
	view := GitReposViaAPI(t, srv, projectID, "")
	if view.ActiveRepoID == "" {
		t.Fatalf("expected active_repo_id, got %#v", view)
	}
	return view.ActiveRepoID
}

func GitRepoURL(projectID, repoID, action string) string {
	return GitReposURL(projectID) + "/" + repoID + "/" + action
}

func GitReposURL(projectID string) string {
	return "/v1/projects/" + projectID + "/git/repos"
}

func GitReposViaAPI(t *testing.T, srv *hostapi.Server, projectID, sessionID string) wire.GitReposView {
	t.Helper()
	url := GitReposURL(projectID)
	if sessionID != "" {
		url += "?session_id=" + sessionID
	}
	req := NewAuthedRequest(http.MethodGet, url, nil)
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

type GitStatusSnapshot struct {
	wire.GitStatusSummary
	Files []wire.GitFileEntry
}

func GitStatusViaAPI(t *testing.T, srv *hostapi.Server, projectID, repoID string, sessionIDs ...string) GitStatusSnapshot {
	t.Helper()
	sessionID := ""
	if len(sessionIDs) > 0 {
		sessionID = sessionIDs[0]
	}
	statusURL := GitRepoURL(projectID, repoID, "status")
	lens := ""
	if sessionID != "" {
		statusURL += "?session_id=" + sessionID
		lens = "&session_id=" + sessionID
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := NewAuthedRequest(http.MethodGet, statusURL, nil)
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
		changesReq := NewAuthedRequest(http.MethodGet,
			fmt.Sprintf("%s?revision=%d&limit=500%s", GitRepoURL(projectID, repoID, "changes"), summary.Revision, lens), nil)
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
		return GitStatusSnapshot{GitStatusSummary: summary, Files: changes.Files}
	}
	t.Fatal("git status did not become ready")
	return GitStatusSnapshot{}
}

func InitCommittedRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		gittest.Run(t, dir, args...)
	}
	gittest.Init(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	return dir, run
}

func InitDirtyRepo(t *testing.T) string {
	t.Helper()
	dir, _ := InitCommittedRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatalf("modify README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	return dir
}

func NewGitBoard() *board.SnapshotBuilder {
	manager := git.NewManager()
	return &board.SnapshotBuilder{Git: manager, StatusCache: git.NewStatusCache(manager)}
}

func NewGitServer(t *testing.T, reg project.Registry, opts ...TestDeps) *hostapi.Server {
	t.Helper()
	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store.NewMemory(), Projects: reg}, Workflow: hostapi.WorkflowDependencies{
		Board: NewGitBoard(), RepoSetCache: git.NewRepoSetCache(git.DefaultStatusCacheTTL)}}
	for _, opt := range opts {
		opt(&deps)
	}
	return hostapi.NewServer(RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken)
}

func NewGitTestServer(t *testing.T, opts ...TestDeps) (*hostapi.Server, string) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := InitDirtyRepo(t)
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open", err)

	drafter := func(d *hostapi.Dependencies) {
		d.Providers.CommitDrafter = compaction.MockSummarizer{text: "feat: drafted subject"}
	}
	srv := NewGitServer(t, reg, append([]TestDeps{drafter}, opts...)...)
	return srv, p.ID
}

// newGitServer serves reg over a memory session store with a real git board
// and repo-set cache; opts adjust the dependencies after those defaults.

func PostGitJSON(t *testing.T, srv *hostapi.Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := NewAuthedRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}
