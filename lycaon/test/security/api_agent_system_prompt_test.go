package security

import (
	"context"
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
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
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
	implementer := workerPersona(t, orchestration.ProfileImplementer)
	if !carriesPersona(sysContent, implementer) {
		t.Fatalf("worker system prompt lacks the implementer title and focus %q:\n%s", implementer.Focus, sysContent)
	}
	if reviewer := workerPersona(t, "plan-reviewer"); carriesPersona(sysContent, reviewer) {
		t.Fatalf("implementer worker received the plan-reviewer persona:\n%s", sysContent)
	}
}
