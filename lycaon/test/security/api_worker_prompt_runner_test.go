package security

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkerLegPromptAppendsSummary(t *testing.T) {
	rec := llmtest.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "implement feature",
			ToolCalls: []llm.MockToolCall{{
				ID:   "w1",
				Name: "command",
				Args: map[string]any{"command": "printf 'package feature\\n' > feature.go"},
			}},
			FollowUpText: "implemented feature X",
		},
		{
			Pattern: ".",
			Text:    "acknowledged",
		},
	}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	h.Start(t)
	mgr := h.Sessions.Manager
	ctx := context.Background()
	projectDir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, projectDir)
	testutil.FailErr(t, "CreateHarnessSession failed", err)
	parentID := parent.ID
	h.SeedProgress(t, ctx, parentID)

	_, err = h.ToolRegistry.Run(
		ctx,
		"task",
		wiring.TaskToolArgs(orchestration.ProfileImplementer, "implement feature", "feature.go"),
		h.CoordinatorToolContext(t, ctx, parent, projectDir),
	)
	testutil.FailErr(t, "task tool failed", err)

	var approvalErr error
	var approvalsResolved int
	var childSessionID string
	var jobID string
	resolver, ok := h.Sessions.Checkpoints.(*hitl.Checkpoints)
	if !ok {
		t.Fatalf("checkpoint manager = %T, want approval option resolver", h.Sessions.Checkpoints)
	}
	testutil.WaitFor(t, "the worker summary is appended", func() bool {
		jobs, listErr := h.Workers.Queue.ListBySession(ctx, parent.ProjectID, parentID)
		if listErr != nil || len(jobs) == 0 {
			return false
		}
		jobID = jobs[0].ID
		childSessionID = jobs[0].ChildSessionID
		if childSessionID == "" {
			return false
		}
		kind := api.CheckpointKindToolApproval
		pending, err := h.Sessions.Checkpoints.ListPending(ctx, childSessionID, &kind)
		if err != nil {
			approvalErr = err
			return false
		}
		for _, checkpoint := range pending {
			_, approvalErr = resolver.Authority.ResolveApprovalOption(
				ctx, childSessionID, checkpoint.ID, "approve_current_action",
			)
			if approvalErr != nil {
				return false
			}
			approvalsResolved++
		}
		msgs, err := mgr.Runner.Transcript.GetMessages(ctx, parentID)
		if err != nil {
			return false
		}
		for _, msg := range msgs {
			if msg.Role != api.MessageRoleTool || msg.WorkerSummary == nil || msg.WorkerSummary.WorkerID != jobID {
				continue
			}
			if strings.Contains(msg.WorkerSummary.Envelope, `<task`) {
				return true
			}
		}
		return false
	})
	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, parentID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	var foundWorkerCard bool
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || msg.WorkerSummary == nil || msg.WorkerSummary.WorkerID != jobID {
			continue
		}
		foundWorkerCard = true
		if !strings.Contains(msg.WorkerSummary.Envelope, `<task`) {
			t.Fatalf("worker card for job %s missing completion envelope: %q", jobID, msg.WorkerSummary.Envelope)
		}
	}
	if !foundWorkerCard {
		t.Fatalf("parent messages missing worker card for job %s: msgs=%+v", jobID, msgs)
	}
}
