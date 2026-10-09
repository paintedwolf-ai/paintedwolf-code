package promptloop

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckDoomLoopUsesRejectFormatter(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	guard := &fakeDoomLoop{allowed: false, count: 3}
	fmttr := guidance.NewStaticRejectFormatter(&guidance.HintConfig{
		HintCodes: map[string]guidance.HintEntry{
			"DOOM_LOOP_REPEAT": {Message: "blocked repeat"},
		},
	})
	loop := NewPromptLoop(PromptLoopDeps{
		Nudges: NudgesDeps{
			DoomLoop: guard,
			FormatDoomLoopReject: func(_ context.Context, _, tool string, _ map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
				data := map[string]any{"count": count, "tool": tool}
				if repeatedCode != "" {
					data["code"] = repeatedCode
				}
				block, ferr := fmttr.Format("DOOM_LOOP_REPEAT", data)
				if ferr != nil {
					return nil, ferr
				}
				return guidance.NewRefusal("DOOM_LOOP_REPEAT", block), nil
			},
		},
		Closeout: CloseoutDeps{
			RejectFmt: fmttr,
		},
	})
	var n int
	err := loop.Nudges.checkDoomLoop(context.Background(), "s1", "response", "read", map[string]any{"path": "x"}, &n)
	if err == nil || !strings.Contains(err.Error(), "Code: DOOM_LOOP_REPEAT") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunExecutesRegisteredTool(t *testing.T) {
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "file-body", nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", ToolCalls: []llm.MockToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "x"}}}},
		{Pattern: ".*", Text: "done"},
	}})
	var appended []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
			Tools:  reg,
			Policy: &recordingToolPolicy{},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
		Model: ModelDeps{
			LLM: client,
		},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				appended = append(appended, msgs...)
				return nil
			},
			UpdateMessage: func(_ context.Context, _, messageID string, msg api.Message) error {
				for i := range appended {
					if appended[i].ID == messageID {
						appended[i] = msg
						return nil
					}
				}
				return nil
			},
		},
	})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild}
	_, err := loop.Run(context.Background(), PromptRunInput{
		SessionID: "s1",
		Session:   sess,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
		ProfileID: "explore_readonly",
		ToolCtx:   tools.ToolContext{SessionID: "s1"},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	foundTool := false
	for _, msg := range appended {
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, "file-body") {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatalf("appended = %+v", appended)
	}
}

func TestPromptLoop_ToolMessageContainsSpecProgress(t *testing.T) {
	guard := &memoryDoomLoopGuard{counts: map[string]map[string]int{}}
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "file-body", nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", FollowUpText: "Read completed.", ToolCalls: []llm.MockToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "x"}}}},
	}})
	hints := &guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{
		"SPEC_POSTURE_PROGRESS": {Message: "progress {{.progress}}"},
	}}
	enricher := guidance.NewToolOutputEnricher(hints, nil)
	var appended []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Limits: func(context.Context, *api.Session) settings.SessionLimits {
				lim := settings.DefaultSessionLimits()
				lim.MaxIterations = 2
				return lim
			},
			Tools:  reg,
			Policy: &recordingToolPolicy{},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
		Model: ModelDeps{
			LLM: client,
		},
		Nudges: NudgesDeps{
			DoomLoop: guard,
		},
		Tools: ToolsDeps{
			EnrichToolOutput: func(_ context.Context, sess *api.Session, tool string, args map[string]any, output string, _ guidance.ToolResultFacts, _ int) (string, guidance.ToolResultFacts) {
				enriched := enricher.Enrich(t.Context(), guidance.EnrichInput{
					SessionID: sess.ID,
					Session:   sess,
					Tool:      tool,
					Args:      args,
					Output:    output,
					PlanProgress: guidance.PlanProgress{
						PhaseInferred:     1,
						PhaseInferredName: "stub",
						NextAction:        "write stub",
						ProgressChecklist: "[ ] Phase 1 — stub\n",
						ChecklistHash:     "h1",
					},
				})
				return enriched.Output, enriched.Facts
			},
		},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				appended = append(appended, msgs...)
				return nil
			},
			UpdateMessage: func(_ context.Context, _, messageID string, msg api.Message) error {
				for i := range appended {
					if appended[i].ID == messageID {
						appended[i] = msg
						return nil
					}
				}
				return nil
			},
		},
	})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec}
	_, err := loop.Run(context.Background(), PromptRunInput{
		SessionID: "s1",
		Session:   sess,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
		ProfileID: "explore_readonly",
		ToolCtx:   tools.ToolContext{SessionID: "s1"},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	hasProgress := false
	for _, msg := range appended {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if strings.Contains(msg.Content, "Code: SPEC_POSTURE_PROGRESS") {
			hasProgress = true
		}
	}
	if !hasProgress {
		t.Fatalf("expected spec progress banner in tool output, appended=%+v", appended)
	}
}

func TestPromptLoop_PhaseGateUnmetJSONSkipsDoomLoopRecord(t *testing.T) {
	guard := &memoryDoomLoopGuard{counts: map[string]map[string]int{}}
	reg := tools.NewStubRegistry()
	_ = reg.Register("workflow_advance", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return `{"error":"phase_gate_unmet","failed_gate":"research_satisfied","phase":"research"}`, nil
	})
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Tools: reg,
		},
		Nudges: NudgesDeps{
			DoomLoop: guard,
			// The banner carries the gate refusal.,
		},
		Tools: ToolsDeps{
			EnrichToolOutput: func(_ context.Context, _ *api.Session, _ string, _ map[string]any, output string, raised guidance.ToolResultFacts, _ int) (string, guidance.ToolResultFacts) {
				return output + "\n\n>>> Tool feedback\nGate blocked\nCode: WORKFLOW_GATE_BLOCKED",
					raised.WithCode("WORKFLOW_GATE_BLOCKED").WithOutcome(api.ToolResultOutcomeRejected)
			},
		},
	})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec}
	_ = loop.Tools.executeToolCall(context.Background(), sess, "s1", "", nil, api.ToolCall{
		Name: "workflow_advance",
		Args: map[string]any{},
	}, tools.ToolContext{SessionID: "s1"}, nil, 0, "", api.CoordinatorRunContext{})
	if got := guard.counts["s1"]; len(got) != 0 {
		t.Fatalf("doom-loop count for gate-blocked advance = %v want empty", got)
	}
}

func TestExecuteToolCallCarriesCompiledInvocationContract(t *testing.T) {
	reg := tools.NewStubRegistry()
	var captured tools.ToolContext
	testutil.FailErr(t, "register read", reg.Register("read", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		captured = tctx
		return "ok", nil
	}))
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Tools: reg,
		},
	})
	loop.Tools.executeToolCall(context.Background(), &api.Session{ID: "s1"}, "s1", "", nil, api.ToolCall{
		ID: "call-1", Name: "read", Args: map[string]any{},
	}, tools.ToolContext{SessionID: "s1"}, nil, 0, "", api.CoordinatorRunContext{})
	want, ok := toolcontract.Lookup("read")
	if !ok {
		t.Fatal("read contract is not declared")
	}
	if !reflect.DeepEqual(captured.Invocation.Contract, want) {
		t.Fatalf("contract = %+v want %+v", captured.Invocation.Contract, want)
	}
}

func TestExecuteToolCallTracksWhetherTheSubsystemOwnerRan(t *testing.T) {
	blocked := tools.NewStubRegistry()
	testutil.FailErr(t, "register blocked read", blocked.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", nil
	}))
	blocked.SetFail("read", errors.New("blocked before subsystem owner"))
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Tools: blocked,
		},
	})
	run := loop.Tools.executeToolCall(t.Context(), &api.Session{ID: "s1"}, "s1", "", nil, api.ToolCall{
		ID: "call-1", Name: "read", Args: map[string]any{},
	}, tools.ToolContext{SessionID: "s1"}, nil, 0, "", api.CoordinatorRunContext{})
	if run.invoked {
		t.Fatal("pre-subsystem-owner rejection marked invoked")
	}

	invoked := tools.NewStubRegistry()
	testutil.FailErr(t, "register failing read", invoked.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", errors.New("subsystem owner failed")
	}))
	loop = NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Tools: invoked,
		},
	})
	run = loop.Tools.executeToolCall(t.Context(), &api.Session{ID: "s1"}, "s1", "", nil, api.ToolCall{
		ID: "call-2", Name: "read", Args: map[string]any{},
	}, tools.ToolContext{SessionID: "s1"}, nil, 0, "", api.CoordinatorRunContext{})
	if !run.invoked {
		t.Fatal("subsystem-owner failure was not marked invoked")
	}
}

func TestSurveyReceiptsReachDoomLoopObservers(t *testing.T) {
	guard := &memoryDoomLoopGuard{}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Nudges: NudgesDeps{
			DoomLoop: guard,
		},
	})
	args := map[string]any{"pattern": "result"}
	loop.Nudges.recordSearchOutcome(t.Context(), "s1", "grep", args, "unstructured result")
	for _, touched := range []int{0, 2} {
		output := surveyreceipt.Attach("result", surveyreceipt.New("grep", ".", touched, 6, false))
		loop.Nudges.recordSearchOutcome(t.Context(), "s1", "grep", args, output)
	}
	want := []bool{false, true}
	if !reflect.DeepEqual(guard.searchOutcomes, want) {
		t.Fatalf("survey observations = %v, want %v", guard.searchOutcomes, want)
	}
}

func TestPreExecutorRefusalsUseOneOccurrenceAndOfferedRecovery(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools: ToolsDeps{
			BlockPlane: &tools.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(nil, nil)},
		},
	})
	for _, code := range []string{"TOOL_NOT_OFFERED", "TOOL_INVOKE_PROSE_TURN"} {
		rule, _ := rules.Get(code)
		rule.OnFire = []oar.OnFireAction{oar.OnFireIncrementCounter}
	}
	for _, tc := range []struct {
		name         string
		code         string
		loadable     bool
		offered      []string
		recovery     string
		wantRecovery bool
	}{
		{"deferred loading offered", "TOOL_NOT_OFFERED", true, []string{"request_tools"}, "request_tools", true},
		{"deferred loading unavailable", "TOOL_NOT_OFFERED", true, []string{"read"}, "request_tools", false},
		{"unavailable name", "TOOL_NOT_OFFERED", false, []string{"request_tools"}, "request_tools", false},
		{"worker completion", "TOOL_INVOKE_PROSE_TURN", false, []string{"complete_leg"}, "complete_leg", true},
		{"coordinator completion", "TOOL_INVOKE_PROSE_TURN", false, []string{}, "complete_leg", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &api.Session{ID: t.Name(), AgentType: "coordinator"}
			var deferred []string
			if tc.loadable {
				deferred = []string{"command"}
			}
			reject := loop.Tools.rejectToolOccurrence(t.Context(), sess, api.ToolCall{Name: "command"}, tools.ToolContext{Agent: "coordinator", TurnOfferedToolNames: tc.offered, TurnToolPlan: toolsurface.Compile(tc.offered, deferred)}, tc.code, nil)
			if reject.Code() != tc.code || reject.Copy == nil || tools.AsToolReject(reject) == nil {
				t.Fatalf("pre-executor refusal lost decision: %+v", reject)
			}
			if strings.Contains(reject.Copy["fix"], tc.recovery) != tc.wantRecovery {
				t.Fatalf("recovery disagrees with offered tools %v: %s", tc.offered, reject.Copy["fix"])
			}
			if tc.code == "TOOL_NOT_OFFERED" {
				data := tools.AsToolReject(reject).Data
				wantLoad := tc.loadable && tc.wantRecovery
				if data["tool_loadable"] != wantLoad {
					t.Fatalf("loading fact = %v, want %v", data, wantLoad)
				}
				if wantLoad {
					want := []tools.ReplacementCall{{Tool: "request_tools", Args: map[string]any{"need": "command"}}}
					if !reflect.DeepEqual(data["replacement_calls"], want) {
						t.Fatalf("loading call = %#v", data["replacement_calls"])
					}
					if !strings.Contains(reject.Copy["instead"], "request_tools") {
						t.Fatalf("next action lost loading recovery: %v", reject.Copy)
					}
				} else if _, exists := data["replacement_calls"]; exists {
					t.Fatalf("unavailable tool has loading remedy: %v", data)
				}
			}
			rule, _ := rules.Get(tc.code)
			if count := pipeline.Counters().Get(sess.ID, rule.Qualified(), oar.CounterFire); count != 1 {
				t.Fatalf("rejection occurrences = %d", count)
			}
		})
	}
}
