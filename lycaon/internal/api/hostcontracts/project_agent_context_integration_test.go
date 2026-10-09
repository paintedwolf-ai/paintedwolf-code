package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectAgentContextUsesSessionWorktree(t *testing.T) {
	srv := contractfixture.TrustSettingsServer(t)
	root := contractfixture.InitCommittedRepoDir(t)
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "CreateWithRoot", err)
	root = p.Roots[0].Path
	sess, err := srv.Sources.Workspace.SessionStore.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create session", err)

	branch, err := git.NewManager().Branch(t.Context(), root)
	testutil.FailErr(t, "read base branch", err)
	worktree := filepath.Join(t.TempDir(), "checkout")
	worktreeBranch := "session/" + sess.ID
	testutil.FailErr(t, "add worktree", git.NewManager().AddWorktree(
		t.Context(), root, worktree, worktreeBranch, branch,
	))
	testutil.FailErr(t, "bind worktree", srv.Sources.Workspace.SessionStore.PutWorktreeBinding(
		t.Context(),
		sessionstore.WorktreeBinding{
			SessionID:    sess.ID,
			ProjectID:    p.ID,
			Toplevel:     root,
			WorktreePath: worktree,
			Branch:       worktreeBranch,
			BaseBranch:   branch,
		},
	))
	testutil.FailErr(t, "write checkout instructions", os.WriteFile(
		filepath.Join(worktree, "AGENTS.md"), []byte("checkout instructions\n"), 0o644,
	))

	load := func(sessionID string) wire.ProjectAgentContext {
		t.Helper()
		path := "/v1/projects/" + p.ID + "/agent-context?root_id=" + p.Roots[0].ID
		if sessionID != "" {
			path += "&session_id=" + sessionID
		}
		req := contractfixture.NewAuthedRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var got wire.ProjectAgentContext
		testutil.FailErr(t, "decode context", json.Unmarshal(w.Body.Bytes(), &got))
		return got
	}

	if got := load(""); len(got.Instructions) != 0 {
		t.Fatalf("base instructions = %+v, want none", got.Instructions)
	}
	got := load(sess.ID)
	if len(got.Instructions) != 1 || got.Instructions[0].Path != "AGENTS.md" {
		t.Fatalf("checkout instructions = %+v", got.Instructions)
	}
}
