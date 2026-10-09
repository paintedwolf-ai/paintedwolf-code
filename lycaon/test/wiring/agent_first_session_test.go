package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFirstPromptAttachesDefaultWorkflow(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".", Text: "I'll help with that."},
	}}))
	h := BuildForTest(t, WithLLMClient(rec))
	ctx := context.Background()
	projectDir := h.ProjectDir(t, "new-session")
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, projectDir)
	testutil.FailErr(t, "h.Store.Create failed", err)
	run, err := h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "read workflow before first prompt", err)
	if run != nil {
		t.Fatal("store-only session fixture already has a workflow")
	}
	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "Let's build a basic python text adventure game."); err != nil {
		testutil.FailErr(t, "h.SessionMgr.Submissions.Prompt failed", err)
	}
	run, err = h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "h.WorkflowMgr.GetActive failed", err)
	if run == nil || !h.WorkflowMgr.IsAmbientRun(run) {
		t.Fatalf("first prompt must attach the default workflow, got %+v", run)
	}
	sess, err = h.Store.Get(ctx, sess.ID)
	testutil.FailErr(t, "read session posture after attachment", err)
	if sess.Posture != wire.SessionPostureBuild {
		t.Fatalf("default workflow posture = %q, want build", sess.Posture)
	}
}

func TestNoWorkflowStartNudgeOnCasualPrompt(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".", Text: "Sure, I can help implement that."},
	}}))
	h := BuildForTest(t, WithLLMClient(rec))
	ctx := context.Background()
	projectDir := h.ProjectDir(t, "casual-prompt")
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, projectDir)
	testutil.FailErr(t, "h.Store.Create failed", err)
	prompt := "Let's build a basic python text adventure game."
	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, prompt); err != nil {
		testutil.FailErr(t, "h.SessionMgr.Submissions.Prompt failed", err)
	}
	msgs, err := h.SessionMgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "h.SessionMgr.GetMessages failed", err)
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[host:workflow-start]") {
			t.Fatalf("casual build prompt must not inject workflow-start nudge, message=%q", msg.Content)
		}
	}
	for _, req := range rec.AllRequests() {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "[host:workflow-start]") {
				t.Fatalf("LLM prompt must not contain workflow-start nudge, message=%q", msg.Content)
			}
		}
	}
}
