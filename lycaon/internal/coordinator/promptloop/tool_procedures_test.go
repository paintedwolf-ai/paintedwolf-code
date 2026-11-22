package promptloop

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolProceduresCloseStandingPrefixBeforeSourceBrief(t *testing.T) {
	l := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			ContextSurfaceDeps: ContextSurfaceDeps{
				ToolProcedures: func(context.Context, *api.Session, string, []string) (string, error) { return "tool procedures", nil },
			},
		},
	})
	messages := []api.Message{
		{Role: api.MessageRoleSystem, Content: "standing", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleSystem, Content: "source brief"},
		{Role: api.MessageRoleUser, Content: "request", PromptCacheBreakpoint: api.PromptCacheTierHistory},
	}
	out, err := l.Tools.appendToolProcedures(t.Context(), &api.Session{ID: "s"}, "coordinator", messages, []tools.ToolMeta{{Name: "read"}})
	testutil.FailErr(t, "append tool procedures", err)
	if len(out) != 4 || out[1].Content != "tool procedures" || out[1].PromptCacheBreakpoint != api.PromptCacheTierStanding || out[0].PromptCacheBreakpoint != api.PromptCacheTierNone || out[2].Content != "source brief" || out[3].PromptCacheBreakpoint != api.PromptCacheTierHistory {
		t.Fatalf("incorrect prefix boundary: %+v", out)
	}
	if messages[0].PromptCacheBreakpoint != api.PromptCacheTierStanding {
		t.Fatal("changed the assembly input")
	}
}

func TestProviderRequestProceduresUseActualToolSet(t *testing.T) {
	active := map[string]bool{}
	var delivered []string
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Policy:      registryTestPolicy{metas: []tools.ToolMeta{{Name: "command"}, {Name: "request_tools"}, {Name: "verify", Deferred: true}}},
			LoadedTools: func(string) map[string]bool { return active },
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ContextSurfaceDeps: ContextSurfaceDeps{
				ToolProcedures: func(_ context.Context, _ *api.Session, _ string, offered []string) (string, error) {
					delivered = slices.Clone(offered)
					if slices.Contains(offered, "verify") {
						return "runner procedure", nil
					}
					return "", nil
				},
			},
		},
	})
	sess := &api.Session{ID: "procedures", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	history := []api.Message{{Role: api.MessageRoleSystem, Content: "stable charter", ContextPinned: true}, {Role: api.MessageRoleUser, Content: "inspect the project"}}
	for _, activated := range []bool{false, true, false, true} {
		active = map[string]bool{}
		if activated {
			active["verify"] = true
		}
		turn, err := loop.Model.buildTurnRequest(t.Context(), sess, sess.ID, history, prompts.CoordinatorProfileID, 0, 20, false, &promptLoopTurnState{})
		testutil.FailErr(t, "build provider request", err)
		wireNames := namesOf(turn.Req.Tools)
		if !slices.Equal(delivered, wireNames) {
			t.Fatalf("procedures saw %v, provider saw %v", delivered, wireNames)
		}
		count := 0
		for _, msg := range turn.Req.Messages {
			if msg.Content == "runner procedure" {
				count++
				if msg.Authority != api.ContentAuthoritySystem || msg.Origin != api.MessageOriginHost {
					t.Fatal("host tool procedures lost instruction authority")
				}
				if !msg.ContextPinned {
					t.Fatal("procedure is not pinned")
				}
			}
		}
		want := 0
		if activated {
			want = 1
		}
		if count != want {
			t.Fatalf("activated=%v: %d procedure blocks, want %d", activated, count, want)
		}
		if turn.Req.Messages[len(turn.Req.Messages)-1].Content != "inspect the project" {
			t.Fatal("procedure displaced the user tail")
		}
	}
	if len(history) != 2 {
		t.Fatal("request assembly mutated history")
	}
}

// Closeout repairs keep the closed turn's tools callable, so the coordinator
// can resume work; only the final prose turn forbids calls, keeping definitions.
func TestCloseoutRepairToolUseFollowsTurnFinality(t *testing.T) {
	retained := guidance.RetainedCloseout{}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}}},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
		Closeout: CloseoutDeps{
			CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout { return retained },
		},
	})
	sess := &api.Session{ID: "repair", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	history := []api.Message{{Role: api.MessageRoleSystem, Content: "charter", ContextPinned: true}, {Role: api.MessageRoleUser, Content: "report"}}
	document := guidance.RetainedCloseout{Active: true, DocumentAttempt: 1, ForcedBy: []string{guidance.ReportFenceUnreadableCode}}
	for _, tc := range []struct {
		name      string
		retained  guidance.RetainedCloseout
		final     bool
		wantTools bool
	}{
		{name: "no repair", wantTools: true},
		{name: "citation repair", retained: guidance.RetainedCloseout{Active: true, Attempt: 1, ForcedBy: []string{guidance.InvestHandleNotObservedCode}}, wantTools: true},
		{name: "document repair", retained: document, wantTools: true},
		{name: "document repair on the final turn", retained: document, final: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retained = tc.retained
			st := &promptLoopTurnState{proseFinish: tc.final}
			turn, err := loop.Model.buildTurnRequest(t.Context(), sess, sess.ID, history, prompts.CoordinatorProfileID, 0, 20, false, st)
			testutil.FailErr(t, "build provider request", err)
			if got := turn.Req.ToolsCallable(); got != tc.wantTools {
				t.Fatalf("callable = %v, want %v", got, tc.wantTools)
			}
			if len(turn.Req.Tools) == 0 {
				t.Fatal("dropped the tool definitions")
			}
			if got := slices.Contains(st.offeredToolNames, "request_tools"); got != tc.wantTools {
				t.Fatalf("offered names = %v, want request_tools offered = %v", st.offeredToolNames, tc.wantTools)
			}
		})
	}
}

// A coordinator's final prose turn keeps its tool definitions, which providers
// validate tool history against, and forbids calling them; it renders no tool
// procedures. A worker's final turn still offers its completion tool.
func TestProseFinishTurnForbidsCoordinatorToolUse(t *testing.T) {
	procedures := 0
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "command"}, {Name: "submit_verdict"}}},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ContextSurfaceDeps: ContextSurfaceDeps{
				ToolProcedures: func(context.Context, *api.Session, string, []string) (string, error) {
					procedures++
					return "procedure", nil
				},
			},
		},
	})
	sess := &api.Session{ID: "closeout", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	history := []api.Message{{Role: api.MessageRoleSystem, Content: "charter", ContextPinned: true}, {Role: api.MessageRoleUser, Content: "survey"}}
	st := &promptLoopTurnState{proseFinish: true}
	turn, err := loop.Model.buildTurnRequest(t.Context(), sess, sess.ID, history, prompts.CoordinatorProfileID, 3, 20, false, st)
	testutil.FailErr(t, "build closeout request", err)
	if turn.Req.ToolUse != modelcall.ToolUseForbidden || len(turn.Req.Tools) == 0 {
		t.Fatalf("closeout tool use = %d with %d tools, want forbidden with definitions kept", turn.Req.ToolUse, len(turn.Req.Tools))
	}
	if procedures != 0 {
		t.Fatal("a turn that forbids tool use rendered tool procedures")
	}
	if st.offeredToolNames == nil || len(st.offeredToolNames) != 0 {
		t.Fatalf("offered names = %v, want an empty settled set", st.offeredToolNames)
	}
}

func TestToolProceduresSurviveContextFit(t *testing.T) {
	loop := NewPromptLoop(PromptLoopDeps{
		Context: ContextDeps{
			ContextSurfaceDeps: ContextSurfaceDeps{
				ToolProcedures: func(context.Context, *api.Session, string, []string) (string, error) { return "runner procedure", nil },
			},
		},
	})
	history := []api.Message{{Role: api.MessageRoleSystem, Content: "charter", ContextPinned: true}}
	for range 50 {
		history = append(history, api.Message{Role: api.MessageRoleAssistant, Content: strings.Repeat("old context ", 100)})
	}
	history = append(history, api.Message{Role: api.MessageRoleUser, Content: "current request"})
	got, err := loop.Tools.appendToolProcedures(t.Context(), &api.Session{}, "coordinator", history, []tools.ToolMeta{{Name: "command"}})
	testutil.FailErr(t, "append procedures", err)
	fitted := compaction.DeterministicFitWithReserve(compaction.CompactionConfig{KeepRecentMessages: 2}, compaction.ContextMessagesFromAPI(got), 100, 0)
	found := false
	for _, msg := range fitted {
		if msg.Content == "runner procedure" {
			found = true
		}
	}
	if !found || len(fitted) >= len(got) {
		t.Fatal("context fit did not retain the procedure while dropping old context")
	}
}

func TestToolProcedureRenderFailureStopsRequest(t *testing.T) {
	want := errors.New("template unavailable")
	loop := NewPromptLoop(PromptLoopDeps{
		Context: ContextDeps{
			ContextSurfaceDeps: ContextSurfaceDeps{
				ToolProcedures: func(context.Context, *api.Session, string, []string) (string, error) { return "", want },
			},
		},
	})
	_, err := loop.Tools.appendToolProcedures(t.Context(), &api.Session{}, "coordinator", nil, []tools.ToolMeta{{Name: "command"}})
	if !errors.Is(err, want) {
		t.Fatalf("render failure = %v", err)
	}
}

func TestDeferredToolPreLoadSchemaMatchesPostLoadRequestUnderNarrowing(t *testing.T) {
	rawCommandSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
			"capability_request": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"process_control": map[string]any{"type": "object"},
					"host_execution":  map[string]any{"type": "object"},
				},
			},
		},
	}
	rawTaskSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_type": map[string]any{"type": "string", "enum": []string{"code-reviewer", "implementer"}},
		},
	}
	active := map[string]bool{}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Policy: registryTestPolicy{metas: []tools.ToolMeta{
				{Name: "request_tools", ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"need": map[string]any{"type": "string"}}}},
				{Name: "command", Deferred: true, ArgsSchema: toolschema.PruneCommandReadOnlySchema(rawCommandSchema)},
				{Name: "task", Deferred: true, ArgsSchema: rawTaskSchema},
			}},
			LoadedTools:      func(string) map[string]bool { return active },
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
	})
	history := []api.Message{{Role: api.MessageRoleUser, Content: "inspect"}}

	workerSess := &api.Session{ID: "worker-narrow", AgentType: "explore_readonly", ParentSessionID: "parent", WorkspacePath: "/tmp/repo"}
	st1 := &promptLoopTurnState{}
	turn1, err := loop.Model.buildTurnRequest(t.Context(), workerSess, workerSess.ID, history, "explore_readonly", 0, 20, false, st1)
	testutil.FailErr(t, "pre-load worker request", err)
	st1.turnToolPlan = turn1.ToolPlan
	preReach := st1.requestReach()
	if preReach.Status("command") != toolreach.Loadable || !preReach.AcceptsField("command", "command") || preReach.AcceptsField("command", "capability_request.process_control") {
		t.Fatalf("unexpected pre-load command reach/fields: status=%d", preReach.Status("command"))
	}
	preCmdSchema, _ := preReach.ToolSchema("command")

	active["command"] = true
	st2 := &promptLoopTurnState{}
	turn2, err := loop.Model.buildTurnRequest(t.Context(), workerSess, workerSess.ID, history, "explore_readonly", 1, 20, false, st2)
	testutil.FailErr(t, "post-load worker request", err)
	st2.turnToolPlan = turn2.ToolPlan
	postReach := st2.requestReach()
	postCmdSchema, ok := postReach.ToolSchema("command")
	if !ok || postReach.Status("command") != toolreach.Callable || !reflect.DeepEqual(preCmdSchema, postCmdSchema) {
		t.Fatalf("pre-load command schema %#v != post-load schema %#v", preCmdSchema, postCmdSchema)
	}

	active = map[string]bool{}
	coordAsk := &api.Session{ID: "coord-ask-narrow", AgentType: prompts.CoordinatorProfileID, Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild, WorkspacePath: "/tmp/repo"}
	frame := inject.CoordinatorTurnFrame{Roster: &inject.AgentRoster{Effective: []string{"code-reviewer", "implementer"}}}
	stAsk1 := &promptLoopTurnState{}
	stAsk1.coordinatorFrame, stAsk1.coordinatorFrameReady = frame, true
	turnAsk1, err := loop.Model.buildTurnRequest(t.Context(), coordAsk, coordAsk.ID, history, prompts.CoordinatorProfileID, 0, 20, false, stAsk1)
	testutil.FailErr(t, "pre-load ask coordinator request", err)
	stAsk1.turnToolPlan = turnAsk1.ToolPlan
	preAskReach := stAsk1.requestReach()
	if preAskReach.Status("task") != toolreach.Loadable || !preAskReach.AcceptsEnumValue("task", "agent_type", "code-reviewer") || preAskReach.AcceptsEnumValue("task", "agent_type", "implementer") {
		t.Fatalf("pre-load ask task enum did not apply read-only spawn narrowing: status=%d", preAskReach.Status("task"))
	}
	preTaskSchema, _ := preAskReach.ToolSchema("task")

	active["task"] = true
	stAsk2 := &promptLoopTurnState{}
	stAsk2.coordinatorFrame, stAsk2.coordinatorFrameReady = frame, true
	turnAsk2, err := loop.Model.buildTurnRequest(t.Context(), coordAsk, coordAsk.ID, history, prompts.CoordinatorProfileID, 1, 20, false, stAsk2)
	testutil.FailErr(t, "post-load ask coordinator request", err)
	stAsk2.turnToolPlan = turnAsk2.ToolPlan
	postTaskSchema, ok := stAsk2.requestReach().ToolSchema("task")
	if !ok || !reflect.DeepEqual(preTaskSchema, postTaskSchema) {
		t.Fatalf("pre-load ask task schema %#v != post-load schema %#v", preTaskSchema, postTaskSchema)
	}
}
