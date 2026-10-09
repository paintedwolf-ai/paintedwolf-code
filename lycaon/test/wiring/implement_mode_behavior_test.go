package wiring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestImplementModeTaskEnqueue(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "add handler",
		ToolCalls: []llm.MockToolCall{{
			ID:   "tc1",
			Name: "task",
			Args: TaskToolArgs("implementer", "Add a // TODO: review here comment at the top of main.go"),
		}},
		FollowUpText: "Queued implementer.",
	}}})
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session in store", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	if sess.AgentType != orchestration.ProfileCoordinator {
		t.Fatalf("agent_type = %q want coordinator", sess.AgentType)
	}
	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "add handler for health check"); err != nil {
		testutil.FailErr(t, "h.Sessions.Manager.Submissions.Prompt failed", err)
	}
	if err := DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID); err != nil {
		testutil.FailErr(t, "DrainPendingWorkerJobs", err)
	}
	run, err := h.Workflows.Manager.Store.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "h.Workflows.Manager.GetActive failed", err)
	if run == nil || run.WorkflowID != "implement" {
		t.Fatalf("expected ambient implement run, got %+v", run)
	}
	if dep, ok := h.Delegations.Manager.Store.DelegationBySessionID(sess.ID); ok && dep != "" {
		t.Fatalf("expected no delegation, got %q", dep)
	}
	foundTask := false
	testutil.WaitFor(t, 8*time.Second, func() bool {
		msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(ctx, sess.ID)
		testutil.FailErr(t, "h.Sessions.Manager.GetMessages failed", err)
		for _, msg := range msgs {
			if msg.WorkerSummary != nil || strings.Contains(msg.Content, `<task job_id="`) {
				foundTask = true
			}
			if msg.Role == wire.MessageRoleTool && strings.Contains(msg.Content, "state_start") {
				t.Fatalf("must not invoke state_start: %q", msg.Content)
			}
		}
		return foundTask
	})
}
