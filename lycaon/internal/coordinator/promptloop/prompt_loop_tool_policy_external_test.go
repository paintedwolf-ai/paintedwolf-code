package promptloop_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoopRuleRejectAppendsToolMessage(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".*",
		ToolCalls: []llm.MockToolCall{{
			ID:   "tc1",
			Name: "read",
			Args: map[string]any{"path": "x"},
		}},
	}}})
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.LLM = client
	deps.Policy = denyToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "explore_readonly"})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	if len(msgs) < 2 || msgs[1].Role != api.MessageRoleTool {
		t.Fatalf("expected tool reject message, got %+v", msgs)
	}
}

func TestPromptLoop_SpecDenyToolMessageShape(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	formatter := guidance.NewToolRejectFormatter(guidance.NewFeedbackDeduper())
	policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		Rules: richDenyRules{out: &rules.RuleOutcome{
			Allowed:           false,
			Code:              "SPEC_POSTURE_DELEGATION_FORBIDDEN",
			RejectCode:        "SPEC_POSTURE_DELEGATION_FORBIDDEN",
			PhaseRequired:     "1",
			PhaseRequiredName: "Research depth (stub plan file)",
			MinRequired:       "complete the plan workflow before delegation tools",
		}},
		RejectFormatter: formatter,
		BlockPlane:      phaseTestBlockPlane(t),
	})
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{
			ID:   "tc1",
			Name: "delegate_dispatch",
			Args: map[string]any{},
		}}},
		{Content: "Plan workflow continues."},
	}}
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.LLM = client
	deps.Policy = listedEvaluatingPolicy{
		metas:     []tools.ToolMeta{{Name: "delegate_dispatch", ArgsSchema: map[string]any{"type": "object"}}},
		evaluator: policy,
	}
	deps.CoordinatorFrame = staticCoordinatorContext{run: api.CoordinatorRunContext{
		WorkflowID: "plan", CurrentPhase: "execute", PhaseCoordinatorSurface: "plan_execute",
	}}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.WorkspacePath = t.TempDir()
	_, err = loop.Run(ctx, promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "coordinator"})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var got string
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && msg.ToolResult != nil &&
			msg.ToolResult.ToolCallID == "tc1" && msg.ToolResult.Outcome == api.ToolResultOutcomeRejected &&
			strings.Contains(msg.Content, "SPEC_POSTURE_DELEGATION_FORBIDDEN") {
			got = msg.Content
			break
		}
	}
	if got == "" {
		t.Fatalf("expected paired spec posture rejection, got %+v", msgs)
	}
	for _, want := range []string{
		">>> Spec posture blocked",
		"Tool: delegate_dispatch",
		"Blocked at: Phase 1 — Research depth (stub plan file)",
		"Progress:",
		"Code: SPEC_POSTURE_DELEGATION_FORBIDDEN",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestLoopDoomLoopRejectKeepsCallResultPair(t *testing.T) {
	guard := loopguard.NewMemoryDoomLoopGuard()
	ctx := context.Background()
	args := map[string]any{"path": "README.md"}
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	for i := 0; i < loopguard.DoomLoopMaxAttempts; i++ {
		_ = guard.RecordAttempt(ctx, sess.ID, uuid.NewString(), "read", args, "", false)
	}
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: args}}},
		{Content: "Trying a different path."},
	}}
	fmttr := guidance.NewStaticRejectFormatter(&guidance.HintConfig{
		HintCodes: map[string]guidance.HintEntry{
			"DOOM_LOOP_REPEAT": {Message: "blocked repeat"},
		},
	})
	deps := promptloop.StoreDeps(store)
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.DoomLoop = guard
	deps.RejectFmt = fmttr
	deps.FormatDoomLoopReject = func(_ context.Context, _, tool string, _ map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
		data := map[string]any{"count": count, "tool": tool}
		if repeatedCode != "" {
			data["code"] = repeatedCode
		}
		block, err := fmttr.Format("DOOM_LOOP_REPEAT", data)
		if err != nil {
			return nil, err
		}
		return guidance.NewRefusal("DOOM_LOOP_REPEAT", block), nil
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)

	var paired bool
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && msg.ToolResult != nil && msg.ToolResult.ToolCallID == "tc1" &&
			msg.ToolResult.Outcome == api.ToolResultOutcomeRejected && strings.Contains(msg.Content, "DOOM_LOOP_REPEAT") {
			paired = true
		}
	}
	if !paired {
		t.Fatalf("expected paired doom loop rejection, got %+v", msgs)
	}

}

func TestLoopTruncatesLargeToolResult(t *testing.T) {
	limits := settings.DefaultSessionLimits()
	limits.MaxToolResultBytes = 20
	store := store.NewMemory()
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return strings.Repeat("x", 100), nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", ToolCalls: []llm.MockToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "x"}}}},
		{Pattern: ".*", Text: "done"},
	}})
	deps := promptloop.StoreDeps(store)
	deps.Limits = func(context.Context, *api.Session) settings.SessionLimits { return limits }
	deps.LLM = client
	deps.Tools = reg
	deps.CoordinatorFrame = investigateCoordinatorContext()
	deps.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	}); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	foundToolResult := false
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		foundToolResult = true
		if !strings.Contains(msg.Content, "truncated") && len(msg.Content) > 20 {
			t.Fatalf("tool result not truncated: len=%d content=%q", len(msg.Content), msg.Content)
		}
	}
	if !foundToolResult {
		t.Fatal("expected read tool result")
	}
}

func TestLoopRejectsUnofferedToolCalls(t *testing.T) {
	for _, withRead := range []bool{false, true} {
		t.Run(fmt.Sprint(withRead), func(t *testing.T) {
			readCalls := 0
			progressCalls := 0
			client := &sequentialLLMClient{completions: []*modelcall.Completion{
				{ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "read", Args: map[string]any{"path": "x.go"}},
					{ID: "tc2", Name: "update_progress", Args: map[string]any{"content": "- [ ] x"}},
				}},
				{Content: "done"},
			}}
			if !withRead {
				client.completions[0].ToolCalls = client.completions[0].ToolCalls[1:]
			}
			mem := store.NewMemory()
			ctx := context.Background()
			sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			sess.ParentSessionID = "parent-1"
			sess.AgentType = orchestration.ProfileImplementer
			reg := tools.NewStubRegistry()
			_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
				readCalls++
				return "ok", nil
			})
			_ = reg.Register("update_progress", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
				progressCalls++
				return `{"status":"updated"}`, nil
			})
			deps := promptloop.StoreDeps(mem)
			deps.LLM = client
			deps.Policy = &fixedToolPolicy{metas: []tools.ToolMeta{{Name: "read"}}}
			deps.Tools = reg
			loop := promptloop.NewPromptLoopForTest(deps)
			result, err := loop.Run(ctx, promptloop.PromptRunInput{
				SessionID: sess.ID,
				Session:   sess,
				History:   userHistory("implement"),
				ProfileID: "implementer",
				ToolCtx: tools.ToolContext{
					Identity: tools.InvocationIdentity{SessionID: sess.ID,
						Agent: "implementer"},
				},
			})
			testutil.FailErr(t, "loop.Run failed", err)
			if (readCalls == 1) != withRead {
				t.Fatalf("read calls = %d withRead=%v", readCalls, withRead)
			}
			if progressCalls != 0 {
				t.Fatalf("unoffered update_progress ran %d times", progressCalls)
			}
			if result.LastAssistantContent != "done" {
				t.Fatalf("content = %q want done", result.LastAssistantContent)
			}
			if len(client.requests) != 2 {
				t.Fatalf("requests = %d, want rejection followed by recovery", len(client.requests))
			}
			seenReject := false
			for _, msg := range client.requests[1].Messages {
				if msg.ToolResult != nil && msg.ToolResult.ToolCallID == "tc2" &&
					msg.ToolResult.Outcome == api.ToolResultOutcomeRejected && slices.Contains(msg.ToolResult.Codes, "TOOL_NOT_OFFERED") {
					seenReject = true
				}
			}
			if !seenReject {
				t.Fatal("next model request lost the unoffered call's structured rejection")
			}
		})
	}
}
