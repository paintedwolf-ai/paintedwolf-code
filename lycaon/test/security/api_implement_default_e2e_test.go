package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func writeTestProjectApprovalsAllowWrite(t *testing.T, dir string) {
	t.Helper()
	overlay := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlay, 0o700); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	body := "rules:\n  - category: tool\n    pattern: write\n    effect: allow\n  - category: tool\n    pattern: edit\n    effect: allow\n"
	if err := os.WriteFile(filepath.Join(overlay, "approvals.yaml"), []byte(body), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}

func TestImplementDefaultParitySmoke(t *testing.T) {
	// This smoke exercises workflow behavior, not confinement.
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "^Add a // TODO: review here comment",
			ToolCalls: []llm.MockToolCall{{
				ID:   "c1",
				Name: "task",
				Args: wiring.TaskToolArgs("implementer", "Add a // TODO: review here comment at the top of main.go"),
			}},
			FollowUpText: "Delegated to implementer to update main.go.",
		},
		{
			Pattern: "Add a // TODO: review here",
			ToolCalls: []llm.MockToolCall{{
				ID:   "w1",
				Name: "write",
				Args: map[string]any{
					"path":    "main.go",
					"content": "// TODO: review here\npackage main\n\nfunc main() {}\n",
				},
			}},
			FollowUpText: wiring.MockWorkerCompletionJSON("complete", "Added the TODO comment at the top of main.go.",
				[]string{"Added the TODO comment to main.go."}),
		},
	}})
	recording := llm.NewRecordingClient(mock)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(recording))

	ctx := context.Background()
	dir := h.ProjectDir(t, "project")
	writeTestProjectApprovalsAllowWrite(t, dir)
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session in store", err)
	wiring.AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	prompt := "Add a // TODO: review here comment at the top of main.go"
	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, prompt); err != nil {
		testutil.FailErr(t, "h.SessionMgr.Submissions.Prompt failed", err)
	}
	if err := wiring.DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID); err != nil {
		last := recording.LastRequest()
		t.Logf("last completion context: %+v", last.Debug)
		for _, message := range last.Messages {
			if message.Role != api.MessageRoleSystem {
				t.Logf("completion message: role=%s kind=%s content=%s tools=%+v", message.Role, message.Kind,
					message.Content[:min(len(message.Content), 800)], message.ToolCalls)
			}
		}
		testutil.FailErr(t, "DrainPendingWorkerJobs", err)
	}
	// The worker writes into an overlay; promotion makes its edits visible in the primary root.
	if err := wiring.PromotePendingWriteOverlays(ctx, h, sess.ProjectID, sess.ID); err != nil {
		testutil.FailErr(t, "PromotePendingWriteOverlays", err)
	}
	data, err := os.ReadFile(mainPath)
	testutil.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "// TODO: review here") {
		t.Fatalf("main.go = %q want TODO comment after worker", string(data))
	}

	run, err := h.WorkflowMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "h.WorkflowMgr.GetActive failed", err)
	if run == nil || run.WorkflowID != "implement" {
		t.Fatalf("expected ambient implement run, got %+v", run)
	}

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs/active", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET active workflow status = %d want 200 body=%s", w.Code, w.Body.String())
	}

	msgs, err := h.SessionMgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "h.SessionMgr.GetMessages failed", err)
	foundTask := false
	for _, msg := range msgs {
		if msg.WorkerSummary != nil && msg.WorkerSummary.WorkerID != "" {
			foundTask = true
		}
		if strings.Contains(msg.Content, `<task job_id="`) && strings.Contains(msg.Content, "implementer") {
			foundTask = true
		}
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, "state_start") {
			t.Fatalf("trace must not include state_start: %q", msg.Content)
		}
	}
	if !foundTask {
		t.Fatal("expected task(implementer) worker completion trace on coordinator session")
	}
}

func TestImplementDefaultDisallowsPlanWriterSpawn(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{
			Pattern: "spawn plan writer",
			ToolCalls: []llm.MockToolCall{{
				ID:   "call_task",
				Name: "task",
				Args: wiring.TaskToolArgs("plan-writer", "Draft a plan for auth refactor", "PLAN.md"),
			}},
			FollowUpText: "task handled",
		}},
	})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	sess := createSessionHTTP(t, h.Server, t.TempDir())
	ctx := context.Background()

	postPromptAndWaitIdle(t, h.Server, sess.ID, "spawn plan writer")

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var toolDeny string
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, "DISALLOWED_AGENT") {
			toolDeny = msg.Content
			break
		}
	}
	if toolDeny == "" {
		t.Fatalf("expected DISALLOWED_AGENT tool denial in messages: %+v", msgs)
	}
}
