package wiring

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFreeChatImplementerTask(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "^Add a // TODO: review here comment",
			ToolCalls: []llm.MockToolCall{{
				ID:   "c1",
				Name: "task",
				Args: TaskToolArgs("implementer", "Add a // TODO: review here comment at the top of main.go"),
			}},
			FollowUpText: "Delegated to implementer.",
		},
		{
			Pattern: "Add a // TODO: review here",
			ToolCalls: []llm.MockToolCall{{
				ID:   "w1",
				Name: "command",
				Args: map[string]any{"command": "printf '%s' '// TODO: review here\npackage main\n\nfunc main() {}\n' > main.go"},
			}},
			FollowUpText: "Added the TODO comment at the top of main.go.",
		},
	}})
	rec := llm.NewRecordingClient(mock)
	h := BuildForTest(t, WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	dir := t.TempDir()
	WriteTestProjectApprovalsAllowWrite(t, dir)
	mainPath := WriteImplementFixture(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session in store", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	prompt := "Add a // TODO: review here comment at the top of main.go"
	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, prompt); err != nil {
		testutil.FailErr(t, "h.Sessions.Manager.Submissions.Prompt failed", err)
	}
	if err := DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID); err != nil {
		testutil.FailErr(t, "DrainPendingWorkerJobs", err)
	}
	if err := PromotePendingWriteOverlays(ctx, h, sess.ProjectID, sess.ID); err != nil {
		testutil.FailErr(t, "PromotePendingWriteOverlays", err)
	}
	data, err := os.ReadFile(mainPath)
	testutil.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "// TODO: review here") {
		t.Fatalf("main.go = %q want TODO comment after worker promote", string(data))
	}

	foundTask := false
	for _, req := range rec.AllRequests() {
		for _, tc := range req.Tools {
			if tc.Name == "task" {
				foundTask = true
			}
		}
	}
	if !foundTask {
		t.Fatal("coordinator completion request must expose task tool")
	}
	run, err := h.Workflows.Manager.Store.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "h.Workflows.Manager.GetActive failed", err)
	if run == nil || run.WorkflowID != "implement" {
		t.Fatalf("expected ambient implement run, got %+v", run)
	}
}

func TestFreeChatImplementerChildIncludesCommandInSchema(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: "implementer",
		Prompt:    "Add a test for main.go",
	})
	testutil.FailErr(t, "SpawnChild", err)
	prof, err := h.AgentRegistry.Get("implementer")
	testutil.FailErr(t, "agents.Get", err)
	policy := h.Sessions.Manager.Coordinator.Guards.Policy()
	hasCommand := false
	for _, meta := range policy.ListForPrompt(ctx, child, prof.ToolProfile) {
		if meta.Name == "command" {
			hasCommand = true
		}
	}
	if !hasCommand {
		t.Fatalf("implementer schema must include command")
	}
}
