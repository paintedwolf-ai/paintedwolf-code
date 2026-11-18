package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestCoordinatorPromptInjectWithoutToolCall(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	rec := h.Recording
	mgr := h.Sessions.Manager
	srv := h.Server
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module e2e.board\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	sess := createSessionHTTP(t, srv, dir)
	if _, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.1",
	}); err != nil {
		testutil.FailErr(t, "h.Workflows.Manager.Starts.StartHuman failed", err)
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
	mgr := h.Sessions.Manager
	srv := h.Server
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module e2e.dedup\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	sess := createSessionHTTP(t, srv, dir)
	exitAmbientRunHTTP(t, srv, sess.ID)

	var injected []bool
	for _, prompt := range []string{"one", "two", "three", "four"} {
		before := len(rec.AllRequests())
		if _, err := mgr.Submissions.Prompt(ctx, sess.ID, prompt); err != nil {
			testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
		}
		reqs := rec.AllRequests()[before:]
		if len(reqs) == 0 {
			t.Fatalf("prompt %q made no coordinator request", prompt)
		}
		carries := false
		for _, req := range reqs {
			carries = carries || boardInjectInMessages(req.Messages)
		}
		if len(injected) == 0 && !boardInjectInMessages(reqs[0].Messages) {
			t.Fatal("the first coordinator request lacks the board inject")
		}
		injected = append(injected, carries)
	}
	// The board reinjects whenever its fingerprint changes, so a later
	// prompt may carry it again; an unchanged board must be left out.
	for _, carries := range injected[1:] {
		if !carries {
			return
		}
	}
	t.Fatalf("board inject never deduplicated across four prompts: %v", injected)
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
	return strings.Contains(content, inject.BoardOrientationInjectSentinel) ||
		strings.Contains(content, packboard.PackBoardSentinel)
}
