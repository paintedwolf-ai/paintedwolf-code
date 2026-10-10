package security

import (
	"context"
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
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "done",
	}}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	mgr := h.Sessions.Manager

	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, t.TempDir())
	testutil.FailErr(t, "create session in store", err)
	r, err := h.Delegations.Manager.Init(ctx, parent.ID, api.CreateDelegationRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Task:      "add handler",
		Strategy:  api.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "h.Delegations.Manager.Init failed", err)
	legID := r.Legs[0].ID
	leg, err := h.Delegations.Manager.DispatchLeg(ctx, r.ID, legID, "")
	testutil.FailErr(t, "h.Delegations.Manager.DispatchLeg failed", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType:   orchestration.ProfileImplementer,
		Prompt:      "add handler",
		WorkerJobID: leg.WorkerID,
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	testutil.FailErr(t, "bind worker child", h.Delegations.Queue.SetChildSessionID(ctx, leg.WorkerID, child.ID))
	// A worker child's turn resolves its queued task from the bound job.
	if _, err := mgr.Submissions.Prompt(workercontext.WithJob(ctx, leg.WorkerID), child.ID, "add handler"); err != nil {
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
