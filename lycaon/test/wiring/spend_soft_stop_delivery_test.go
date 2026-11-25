package wiring

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/session/turnnudges"
	"github.com/lycaon/lycaon/internal/settings"
	setupanchor "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type spendSoftStopCaptureLLM struct {
	completions []*modelcall.Completion
	requests    []modelcall.CompletionRequest
	index       int
}

func (c *spendSoftStopCaptureLLM) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.index >= len(c.completions) {
		return nil, fmt.Errorf("unexpected model call %d", c.index+1)
	}
	out := *c.completions[c.index]
	c.index++
	return &out, nil
}

func (c *spendSoftStopCaptureLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.requests = append(c.requests, req)
	ch := make(chan modelcall.StreamChunk, 1)
	completion, err := c.Complete(ctx, req)
	if err != nil {
		close(ch)
		return ch, err
	}
	ch <- modelcall.StreamChunk{
		Content:   completion.Content,
		ToolCalls: completion.ToolCalls,
		Done:      true,
	}
	close(ch)
	return ch, nil
}

type spendSoftStopToolPolicy struct {
	metas []tools.ToolMeta
}

func (p spendSoftStopToolPolicy) ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta {
	return slices.Clone(p.metas)
}

func (spendSoftStopToolPolicy) EvaluateInvoke(context.Context, *api.Session, string, map[string]any) error {
	return nil
}

type spendSoftStopSurface string

func (s spendSoftStopSurface) PromptTurnSurfaceID(string) string { return string(s) }

type spendSoftStopNoopStreams struct {
	repos *store.Repositories
}

func (s *spendSoftStopNoopStreams) CacheLive(string, string, string, []string, int) {}
func (s *spendSoftStopNoopStreams) CacheReplay(string, string, []string)            {}
func (s *spendSoftStopNoopStreams) Finish(context.Context, string)                  {}
func (s *spendSoftStopNoopStreams) Project(ctx context.Context, sessionID string, msg api.Message) error {
	return s.repos.Transcript.PatchLiveProjection(ctx, sessionID, msg.ID, msg.Content, msg.ToolCalls)
}

type spendSoftStopEmptyEvidence struct{}

func (spendSoftStopEmptyEvidence) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, nil
}

func (spendSoftStopEmptyEvidence) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

type forcedErrorSpendSoftStopEngine struct {
	prompts.PromptTemplateEngine
	forcedCalls int
}

func (e *forcedErrorSpendSoftStopEngine) RenderKick(ctx context.Context, reader prompts.Reader, name string, data map[string]any) (string, error) {
	if strings.TrimSuffix(strings.TrimSpace(name), ".md") == "coordinator-spend-soft-stop" {
		e.forcedCalls++
		return "PARTIAL-SOFT-STOP-MUST-NOT-DELIVER", fmt.Errorf("forced failure: %w", pongoplain.ErrExecutionLimit)
	}
	return e.PromptTemplateEngine.RenderKick(ctx, reader, name, data)
}

func requestToolNames(req modelcall.CompletionRequest) []string {
	out := make([]string, 0, len(req.Tools))
	for _, meta := range req.Tools {
		out = append(out, meta.Name)
	}
	slices.Sort(out)
	return out
}

func hostInstructionsInRequest(req modelcall.CompletionRequest) []string {
	var out []string
	for _, msg := range req.Messages {
		if msg.Origin == api.MessageOriginHost {
			out = append(out, msg.Content)
		}
	}
	return out
}

func newSpendSoftStopLoopFixture(
	t *testing.T,
	promptEngine prompts.PromptTemplateEngine,
	client *spendSoftStopCaptureLLM,
) (*promptloop.PromptLoop, *api.Session, *[]string) {
	t.Helper()
	setupanchor.Install()

	workspaceDir := t.TempDir()
	repos := storetest.New(t, "proj-spend-soft-stop", workspaceDir)
	sess, err := repos.Sessions.Create(
		t.Context(),
		api.CreateSessionRequest{Posture: api.SessionPostureBuild},
		workspaceDir,
	)
	testutil.FailErr(t, "create session", err)

	ledger := turnload.NewLedger()
	toolNames := []string{"read", "command", "task", "instruct_worker", "answer_decision"}
	ledger.Activate(sess.ID, toolNames, "active coordinator surface tools")

	nudgeSvc := turnnudges.New(ledger, nil)
	nudgeSvc.SetPrompts(promptEngine)
	nudgeSvc.SetSurface(spendSoftStopSurface("implement_dispatch"))

	var executed []string
	registry := tools.NewDefaultRegistry()
	metas := make([]tools.ToolMeta, 0, len(toolNames))
	for _, toolName := range toolNames {
		name := toolName
		metas = append(metas, tools.ToolMeta{Name: name})
		regErr := registry.Register(name, func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
			path, _ := args["path"].(string)
			executed = append(executed, fmt.Sprintf("%s(%s)", name, path))
			return fmt.Sprintf("ok:%s:%s", name, path), nil
		})
		testutil.FailErr(t, "register "+name, regErr)
	}

	ceilingErr := errors.New("session spend ceiling reached")
	var ceilingChecks int

	loop := promptloop.NewPromptLoop(promptloop.PromptLoopDeps{
		Context: promptloop.ContextDeps{
			ResolveEffectiveMode: repos.ResolveEffectiveMode,
			Limits: func(context.Context, *api.Session) settings.SessionLimits {
				return settings.DefaultSessionLimits()
			},
			Tools:  registry,
			Policy: spendSoftStopToolPolicy{metas: metas},
			LoadedTools: func(sessionID string) map[string]bool {
				return ledger.Active(sessionID)
			},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
			ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
				PromptTurnSurface: func(string) string { return "implement_dispatch" },
			},
		},
		Model: promptloop.ModelDeps{
			LLM: client,
		},
		Closeout: promptloop.CloseoutDeps{
			EvidenceLedger: spendSoftStopEmptyEvidence{},
			EvaluateCloseoutBlock: func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) {
				return nil, nil
			},
			CloseoutTrackingDeps: promptloop.CloseoutTrackingDeps{
				TurnCloseoutNudge: nudgeSvc.Closeout,
			},
		},
		Projection: promptloop.ProjectionDeps{
			AppendMessages: repos.Transcript.AppendMessages,
			UpdateMessage: func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
				_, uErr := repos.Transcript.UpdateMessage(ctx, sessionID, messageID, msg)
				return uErr
			},
			Streams:                     &spendSoftStopNoopStreams{repos: repos},
			AppendDraftVersion:          repos.Transcript.AppendDraftVersion,
			CountDraftVersions:          repos.Transcript.CountDraftVersions,
			SettledModelOutputIteration: repos.Turns.SettledModelOutputIteration,
			AdmitModelResponse:          func(context.Context, string, string) error { return nil },
		},
		Nudges: promptloop.NudgesDeps{
			CheckSpendCeiling: func(context.Context, string, *api.Session) (promptloop.SpendCeilingCheck, error) {
				ceilingChecks++
				if ceilingChecks == 1 {
					return promptloop.SpendCeilingCheck{}, nil
				}
				return promptloop.SpendCeilingCheck{SoftStop: true}, ceilingErr
			},
			IsSpendCeiling:     func(err error) bool { return errors.Is(err, ceilingErr) },
			SpendSoftStopNudge: nudgeSvc.SpendSoftStop,
		},
	})

	return loop, sess, &executed
}

func TestSpendSoftStopDelivery(t *testing.T) {
	stockEngine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: configlayout.FindModuleRoot(),
	})

	t.Run("rendered_soft_stop_grants_bounded_wind_down", func(t *testing.T) {
		client := &spendSoftStopCaptureLLM{
			completions: []*modelcall.Completion{
				{
					ToolCalls: []api.ToolCall{
						{Name: "read", ID: "call-initial", Args: map[string]any{"path": "initial.go"}},
					},
				},
				{
					ToolCalls: []api.ToolCall{
						{Name: "read", ID: "call-wind-down-read", Args: map[string]any{"path": "final.go"}},
						{Name: "task", ID: "call-wind-down-task", Args: map[string]any{"path": "should-drop"}},
					},
				},
				{
					Content: "Completed bounded wind-down and summarized what remains.",
				},
			},
		}
		loop, sess, executed := newSpendSoftStopLoopFixture(t, stockEngine, client)

		res, err := loop.Run(t.Context(), promptloop.PromptRunInput{
			TurnID:    "turn-soft-stop-ok",
			AttemptID: "attempt-soft-stop-ok",
			SessionID: sess.ID,
			Session:   sess,
			History:   []api.Message{{Role: api.MessageRoleUser, Content: "Finish the pending edit."}},
			ProfileID: "implement",
			ToolCtx: tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: sess.ID, ProjectID: sess.ProjectID},
			},
		})
		testutil.FailErr(t, "loop.Run", err)

		if len(client.requests) != 3 {
			t.Fatalf("model calls = %d, want 3", len(client.requests))
		}

		initialTools := requestToolNames(client.requests[0])
		if !slices.Contains(initialTools, "task") || !slices.Contains(initialTools, "read") {
			t.Fatalf("initial tools = %v, want both read and task", initialTools)
		}

		windDownReq := client.requests[1]
		windDownTools := requestToolNames(windDownReq)
		windDownInstructions := hostInstructionsInRequest(windDownReq)

		if windDownReq.ToolUse != modelcall.ToolUseAllowed {
			t.Fatalf("wind-down ToolUse = %v, want %v", windDownReq.ToolUse, modelcall.ToolUseAllowed)
		}
		if !slices.Contains(windDownTools, "read") || !slices.Contains(windDownTools, "command") {
			t.Fatalf("wind-down Tools = %v, want read and command retained", windDownTools)
		}
		for _, withheld := range []string{"task", "instruct_worker", "answer_decision"} {
			if slices.Contains(windDownTools, withheld) {
				t.Fatalf("wind-down Tools retained withheld delegation tool %q: %v", withheld, windDownTools)
			}
		}
		if !slices.ContainsFunc(windDownInstructions, func(s string) bool {
			return strings.Contains(s, "[host:coordinator-spend-soft-stop]")
		}) {
			t.Fatalf("wind-down request missing rendered coordinator-spend-soft-stop instruction: %v", windDownInstructions)
		}

		wantExecuted := []string{"read(initial.go)", "read(final.go)"}
		if !slices.Equal(*executed, wantExecuted) {
			t.Fatalf("executed tools = %v, want %v (task must be dropped before execution)", *executed, wantExecuted)
		}

		closeoutReq := client.requests[2]
		closeoutInstructions := hostInstructionsInRequest(closeoutReq)

		if closeoutReq.ToolUse != modelcall.ToolUseForbidden {
			t.Fatalf("final closeout ToolUse = %v, want %v", closeoutReq.ToolUse, modelcall.ToolUseForbidden)
		}
		if !slices.ContainsFunc(closeoutInstructions, func(s string) bool {
			return strings.Contains(s, "[host:coordinator-turn-closeout]")
		}) {
			t.Fatalf("final closeout missing rendered coordinator-turn-closeout instruction: %v", closeoutInstructions)
		}
		if res.LastAssistantContent != "Completed bounded wind-down and summarized what remains." {
			t.Fatalf("LastAssistantContent = %q", res.LastAssistantContent)
		}
	})

	t.Run("forced_render_error_withholds_wind_down_and_enforces_zero_tool_closeout", func(t *testing.T) {
		forcedEngine := &forcedErrorSpendSoftStopEngine{PromptTemplateEngine: stockEngine}
		client := &spendSoftStopCaptureLLM{
			completions: []*modelcall.Completion{
				{
					ToolCalls: []api.ToolCall{
						{Name: "read", ID: "call-initial", Args: map[string]any{"path": "initial.go"}},
					},
				},
				{
					Content: "Closed out directly after reaching the session spend ceiling.",
					ToolCalls: []api.ToolCall{
						{Name: "read", ID: "stray-closeout-read", Args: map[string]any{"path": "stray.go"}},
						{Name: "task", ID: "stray-closeout-task", Args: map[string]any{"path": "stray-task"}},
					},
				},
			},
		}
		loop, sess, executed := newSpendSoftStopLoopFixture(t, forcedEngine, client)

		res, err := loop.Run(t.Context(), promptloop.PromptRunInput{
			TurnID:    "turn-soft-stop-err",
			AttemptID: "attempt-soft-stop-err",
			SessionID: sess.ID,
			Session:   sess,
			History:   []api.Message{{Role: api.MessageRoleUser, Content: "Finish the pending edit."}},
			ProfileID: "implement",
			ToolCtx: tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: sess.ID, ProjectID: sess.ProjectID},
			},
		})
		testutil.FailErr(t, "loop.Run", err)

		if forcedEngine.forcedCalls != 1 {
			t.Fatalf("forced RenderKick calls = %d, want 1", forcedEngine.forcedCalls)
		}
		if len(client.requests) != 2 {
			t.Fatalf("model calls = %d, want 2 (initial tool turn + prose-only closeout)", len(client.requests))
		}

		for idx, req := range client.requests {
			for _, msg := range req.Messages {
				if strings.Contains(msg.Content, "coordinator-spend-soft-stop") ||
					strings.Contains(msg.Content, "PARTIAL-SOFT-STOP-MUST-NOT-DELIVER") {
					t.Fatalf("request[%d] leaked failed spend-soft-stop content: %q", idx, msg.Content)
				}
			}
		}

		closeoutReq := client.requests[1]
		closeoutInstructions := hostInstructionsInRequest(closeoutReq)

		if closeoutReq.ToolUse != modelcall.ToolUseForbidden {
			t.Fatalf("closeout ToolUse = %v, want %v", closeoutReq.ToolUse, modelcall.ToolUseForbidden)
		}
		if !slices.ContainsFunc(closeoutInstructions, func(s string) bool {
			return strings.Contains(s, "[host:coordinator-turn-closeout]")
		}) {
			t.Fatalf("closeout missing rendered coordinator-turn-closeout instruction: %v", closeoutInstructions)
		}

		wantExecuted := []string{"read(initial.go)"}
		if !slices.Equal(*executed, wantExecuted) {
			t.Fatalf("executed tools = %v, want %v (stray closeout tool calls must not execute)", *executed, wantExecuted)
		}
		if res.LastAssistantContent != "Closed out directly after reaching the session spend ceiling." {
			t.Fatalf("LastAssistantContent = %q", res.LastAssistantContent)
		}
	})
}
