package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestCoordinatorPromptInjectWithoutToolCall(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	rec := h.Recording
	mgr := h.SessionMgr
	srv := h.Server
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module e2e.board\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	sess := createSessionHTTP(t, srv, dir)
	if _, err := h.WorkflowMgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "coordinate"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	// Workflow startup can consume the orientation fingerprint before the user prompt.
	found := false
	for _, req := range rec.AllRequests() {
		if boardInjectInMessages(req.Messages) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("coordinator prompt missing pack board inject")
	}
}

func TestCoordinatorPromptInjectDedup(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	rec := h.Recording
	mgr := h.SessionMgr
	srv := h.Server
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module e2e.dedup\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	sess := createSessionHTTP(t, srv, dir)
	exitAmbientRunHTTP(t, srv, sess.ID)
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "one"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	for _, prompt := range []string{"two", "three", "four"} {
		if _, err := mgr.Submissions.Prompt(ctx, sess.ID, prompt); err != nil {
			testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
		}
		if !boardInjectInMessages(rec.LastRequest().Messages) {
			return
		}
	}
	t.Fatal("expected board injects to deduplicate after repo orientation settled")
}

func boardInjectInMessages(msgs []api.Message) bool {
	for _, m := range msgs {
		if m.Role == api.MessageRoleSystem && boardInjectMarker(m.Content) {
			return true
		}
	}
	return false
}

func boardInjectMarker(content string) bool {
	return strings.Contains(content, "lycaon-board-orientation:v1") ||
		strings.Contains(content, "pack-board:v1")
}
