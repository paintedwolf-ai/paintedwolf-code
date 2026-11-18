package security

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkerSpawnUsesAgentSystemPrompt(t *testing.T) {
	ctx := context.Background()
	rec := llmtest.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "done",
	}}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	mgr := h.Sessions.Manager

	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create session in store", err)
	jobID, err := h.Workers.Queue.Enqueue(ctx, api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		ParentSessionID: parent.ID,
		AgentType:       orchestration.ProfileImplementer,
		Prompt:          "add handler",
		Brief:           "add handler",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "Enqueue failed", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType:   orchestration.ProfileImplementer,
		Prompt:      "add handler",
		WorkerJobID: jobID,
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	testutil.FailErr(t, "bind worker child", h.Workers.Queue.SetChildSessionID(ctx, jobID, child.ID))
	// A worker child's turn resolves its queued task from the bound job.
	if _, err := mgr.Submissions.Prompt(workercontext.WithJob(ctx, jobID), child.ID, "add handler"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}

	var sysContent string
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem {
			sysContent = msg.Content
			break
		}
	}
	if sysContent == "" {
		t.Fatal("expected system message on worker prompt")
	}
	if !strings.Contains(sysContent, "Implementer") {
		t.Fatalf("system prompt = %q want rendered implementer persona", sysContent)
	}
}
