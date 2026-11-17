package security

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
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
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "CreateHarnessSession failed", err)
	h.SeedProgress(t, ctx, parent.ID)

	_, err = h.ToolRegistry.Run(
		ctx,
		"task",
		wiring.TaskToolArgs(orchestration.ProfileImplementer, "implement feature"),
		h.CoordinatorToolContext(t, ctx, parent, dir),
	)
	testutil.FailErr(t, "task tool failed", err)

	testutil.WaitFor(t, "the worker job completes and its outcome is delivered", func() bool {
		wiring.FailOnEndedWorker(t, h, ctx, parent.ProjectID)
		jobs, err := h.Workers.Queue.ListBySession(ctx, parent.ProjectID, parent.ID, api.WorkerStatusComplete)
		if err != nil || len(jobs) != 1 {
			return false
		}
		pending, err := h.Workers.Queue.ListPendingOutcomes(ctx)
		return err == nil && len(pending) == 0
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
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "CreateHarnessSession failed", err)
	h.SeedProgress(t, ctx, parent.ID)

	_, err = h.ToolRegistry.Run(
		ctx,
		"task",
		wiring.TaskToolArgs("plan-reviewer", "review plan"),
		h.CoordinatorToolContext(t, ctx, parent, dir),
	)
	testutil.FailErr(t, "task tool failed", err)

	testutil.WaitFor(t, "the worker job completes and its outcome is delivered", func() bool {
		jobs, err := h.Workers.Queue.ListBySession(ctx, parent.ProjectID, parent.ID, api.WorkerStatusComplete)
		if err != nil || len(jobs) != 1 {
			return false
		}
		pending, err := h.Workers.Queue.ListPendingOutcomes(ctx)
		return err == nil && len(pending) == 0
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
