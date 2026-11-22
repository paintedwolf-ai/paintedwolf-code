package promptloop

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
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

func TestAppendToolProceduresPassesPartialAndEmptyProjectedSchemaMaps(t *testing.T) {
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load anchor registry", err)
	t.Cleanup(anchor.SetAnchorsFor(func(context.Context, string) *anchor.Registry { return reg }))
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load profiles", err)
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: configlayout.FindModuleRoot(),
	}))
	narrowedVerify := map[string]any{
		"type":       "object",
		"properties": map[string]any{"socks_proxy": map[string]any{"type": "boolean"}},
	}
	var (
		gotCtxMap map[string]map[string]any
		gotReader prompts.Reader
	)
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			ContextSurfaceDeps: ContextSurfaceDeps{
				ToolProcedures: func(ctx context.Context, sess *api.Session, _ string, names []string) (string, error) {
					gotCtxMap = prompts.ProjectedSchemasFromContext(ctx)
					loaded := make(map[string]bool, len(names))
					for _, n := range names {
						loaded[n] = true
					}
					r, rErr := prompts.NewReader(prompts.ReaderInput{
						ProfileID:        "implement",
						Profiles:         profiles,
						Plan:             toolsurface.Compile(names, nil),
						Loaded:           loaded,
						Visible:          names,
						ProjectedSchemas: gotCtxMap,
						RootCount:        1,
						WebSearch:        true,
					})
					if rErr != nil {
						return "", rErr
					}
					gotReader = r
					return inject.RenderToolProceduresBlock(ctx, renderer, sess.ID, r)
				},
			},
		},
	})

	out, err := loop.Tools.appendToolProcedures(
		t.Context(),
		&api.Session{ID: "s"},
		"implementer",
		[]api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		[]tools.ToolMeta{
			{Name: "verify", ArgsSchema: narrowedVerify},
			{Name: "command", ArgsSchema: nil},
		},
	)
	testutil.FailErr(t, "append partial schemas", err)
	if gotCtxMap == nil || len(gotCtxMap) != 1 || gotCtxMap["verify"] == nil {
		t.Fatalf("partial projected map = %#v, want non-nil map retaining verify only", gotCtxMap)
	}
	if _, hasCmd := gotCtxMap["command"]; hasCmd {
		t.Fatalf("nil command schema populated an entry: %#v", gotCtxMap)
	}
	facts := gotReader.Facts()
	if got, _ := facts["runner_socks_proxy_tools"].([]string); !slices.Equal(got, []string{"verify"}) {
		t.Fatalf("runner_socks_proxy_tools = %v, want [verify]", got)
	}
	if got, _ := facts["runner_capability_request_tools"].([]string); len(got) != 0 {
		t.Fatalf("runner_capability_request_tools = %v, want empty", got)
	}
	if facts["can_command_verify"] != true {
		t.Fatalf("can_command_verify = %v, want true", facts["can_command_verify"])
	}
	procMsg := out[0].Content
	if !strings.Contains(procMsg, "`socks_proxy: true` on `verify`") || strings.Contains(procMsg, "Declare access needs in `capability_request`") || !strings.Contains(procMsg, "Use `command(verification: true)`") {
		t.Fatalf("rendered procedure block mismatch:\n%s", procMsg)
	}

	_, err = loop.Tools.appendToolProcedures(
		t.Context(),
		&api.Session{ID: "s"},
		"implementer",
		[]api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		[]tools.ToolMeta{{Name: "command", ArgsSchema: nil}},
	)
	testutil.FailErr(t, "append empty schemas", err)
	if gotCtxMap == nil || len(gotCtxMap) != 0 {
		t.Fatalf("empty projected map = %#v, want non-nil empty map", gotCtxMap)
	}
}
