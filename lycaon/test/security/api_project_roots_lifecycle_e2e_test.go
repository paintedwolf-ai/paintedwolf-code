package security

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestDetachThenReadDenied(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:      "read the note",
		ToolCalls:    []llm.MockToolCall{{ID: "call_read", Name: "command", Args: map[string]any{"command": "cat note.txt"}}},
		FollowUpText: "done",
	}}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	srv := httptest.NewServer(h.Server)
	t.Cleanup(srv.Close)
	base := srv.URL

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("detached-note-sentinel"), 0o600); err != nil {
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

	if _, err := h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "read the note"); err != nil {
		testutil.FailErr(t, "prompt after detach", err)
	}
	msgs, err := h.Store.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "get messages", err)
	var result *wire.ToolResult
	for _, m := range msgs {
		if m.ToolResult != nil && m.ToolResult.ToolCallID == "call_read" {
			result = m.ToolResult
		}
	}
	if result == nil {
		t.Fatal("the read after detach produced no tool result row")
	}
	if result.Outcome != wire.ToolResultOutcomeRejected || !slices.Contains(result.Codes, "PROJECT_HAS_NO_ROOTS") {
		t.Fatalf("read after detach = outcome %q codes %v, want rejected with PROJECT_HAS_NO_ROOTS", result.Outcome, result.Codes)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "detached-note-sentinel") {
			t.Fatalf("the detached root's file content reached the transcript: %+v", m)
		}
	}
}
