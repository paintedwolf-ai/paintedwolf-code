package security

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestDetachThenReadDenied(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := httptest.NewServer(h.Server)
	t.Cleanup(srv.Close)
	base := srv.URL

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o600); err != nil {
		testutil.FailErr(t, "write fixture file", err)
	}

	project := createAPIProjectAtPath(t, base, dir)
	if project.RootsGeneration != 0 {
		t.Fatalf("initial generation = %d want 0", project.RootsGeneration)
	}
	rootID := project.Roots[0].ID

	sess := openAPIPostJSON[struct {
		ID string `json:"id"`
	}](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	req := authedRequest(t, http.MethodDelete, "/v1/projects/"+project.ID+"/roots/"+rootID, nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("detach status = %d body = %s", w.Code, w.Body.String())
	}

	updated := openAPIGetJSON[struct {
		Roots           []any `json:"roots"`
		RootsGeneration int   `json:"roots_generation"`
	}](t, base, "/v1/projects/{id}", map[string]string{"id": project.ID}, http.StatusOK)
	if len(updated.Roots) != 0 {
		t.Fatalf("roots = %d want 0 after detach", len(updated.Roots))
	}
	if updated.RootsGeneration != 1 {
		t.Fatalf("roots_generation = %d want 1", updated.RootsGeneration)
	}

	gotSess, err := h.Store.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session", err)
	if gotSess.WorkspaceRootID != "" {
		t.Fatalf("workspace_root_id = %q want empty", gotSess.WorkspaceRootID)
	}

	_, err = h.ToolRegistry.Run(t.Context(), "read", map[string]any{"path": "note.txt"}, tools.ToolContext{
		ProjectID: project.ID,
		SessionID: sess.ID,
		Roots:     nil,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "PROJECT_HAS_NO_ROOTS" {
		t.Fatalf("read err = %v want PROJECT_HAS_NO_ROOTS", err)
	}
}
