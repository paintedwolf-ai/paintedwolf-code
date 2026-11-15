package security

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestDelegateImplementerPromptContainsPersona(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "implementation complete",
	}}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	ctx := context.Background()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, t.TempDir())
	testutil.FailErr(t, "CreateHarnessSession failed", err)
	h.SeedProgress(t, ctx, parent.ID)

	_, err = h.ToolRegistry.Run(
		ctx,
		"task",
		wiring.TaskToolArgs(orchestration.ProfileImplementer, "implement feature"),
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: parent.ID}},
	)
	testutil.FailErr(t, "task tool failed", err)

	testutil.WaitFor(t, func() bool {
		jobs, err := h.Delegations.Queue.ListBySession(ctx, parent.ProjectID, parent.ID, api.WorkerStatusComplete)
		return err == nil && len(jobs) == 1 && jobs[0].OutcomeDeliveredAt != nil
	})

	var hasFocus, hasLeg bool
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role != api.MessageRoleSystem {
			continue
		}
		if strings.Contains(msg.Content, "## Focus") {
			hasFocus = true
		}
		if strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
			hasLeg = true
		}
	}
	if !hasFocus || !hasLeg {
		t.Fatalf("worker prompt missing persona layers: focus=%v leg=%v messages=%d", hasFocus, hasLeg, len(rec.LastRequest().Messages))
	}
}

func TestDelegatePlanReviewerDistinctFromImplementer(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "implementation complete",
	}}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	ctx := context.Background()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, t.TempDir())
	testutil.FailErr(t, "CreateHarnessSession failed", err)
	h.SeedProgress(t, ctx, parent.ID)

	_, err = h.ToolRegistry.Run(
		ctx,
		"task",
		wiring.TaskToolArgs("plan-reviewer", "review plan"),
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: parent.ID}},
	)
	testutil.FailErr(t, "task tool failed", err)

	testutil.WaitFor(t, func() bool {
		jobs, err := h.Delegations.Queue.ListBySession(ctx, parent.ProjectID, parent.ID, api.WorkerStatusComplete)
		return err == nil && len(jobs) == 1 && jobs[0].OutcomeDeliveredAt != nil
	})

	var l1 string
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && !strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
			l1 = msg.Content
			break
		}
	}
	if !strings.Contains(l1, "advisory") {
		t.Fatalf("plan-reviewer L1 = %q", l1)
	}
}
