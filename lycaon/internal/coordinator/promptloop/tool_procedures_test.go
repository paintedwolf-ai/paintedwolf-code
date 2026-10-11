package promptloop

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolProceduresCloseStandingPrefixBeforeSourceBrief(t *testing.T) {
	l := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			ToolProcedures: func(context.Context, *api.Session, string, []string) (string, error) { return "tool procedures", nil },
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
			Policy:      registryTestPolicy{metas: []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}, {Name: "command", Deferred: true}}},
			LoadedTools: func(string) map[string]bool { return active },
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ToolProcedures: func(_ context.Context, _ *api.Session, _ string, offered []string) (string, error) {
				delivered = slices.Clone(offered)
				if slices.Contains(offered, "command") {
					return "runner procedure", nil
				}
				return "", nil
			},
		},
	})
	sess := &api.Session{ID: "procedures", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	history := []api.Message{{Role: api.MessageRoleSystem, Content: "stable charter", ContextPinned: true}, {Role: api.MessageRoleUser, Content: "inspect the project"}}
	for _, activated := range []bool{false, true, false, true} {
		active = map[string]bool{}
		if activated {
			active["command"] = true
		}
		turn, err := loop.Model.buildTurnRequest(t.Context(), sess, sess.ID, history, prompts.CoordinatorProfileID, "inspect", 0, 20, false, &promptLoopTurnState{})
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

// A report-document repair asks only for the fence, so its turn forbids tool
// use while keeping the definitions; a citation repair keeps the surface's tools.
func TestReportDocumentRepairForbidsToolUse(t *testing.T) {
	retained := guidance.RetainedCloseout{}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "read"}, {Name: "update_progress"}}},
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
	for _, tc := range []struct {
		retained  guidance.RetainedCloseout
		wantTools bool
	}{
		{guidance.RetainedCloseout{}, true},
		{guidance.RetainedCloseout{Active: true, Attempt: 1, ForcedBy: []string{guidance.InvestHandleNotObservedCode}}, true},
		{guidance.RetainedCloseout{Active: true, DocumentAttempt: 1, ForcedBy: []string{guidance.ReportClaimUnreportedCode}}, false},
	} {
		retained = tc.retained
		turn, err := loop.Model.buildTurnRequest(t.Context(), sess, sess.ID, history, prompts.CoordinatorProfileID, "report", 0, 20, false, &promptLoopTurnState{})
		testutil.FailErr(t, "build provider request", err)
		if got := turn.Req.ToolsCallable(); got != tc.wantTools {
			t.Fatalf("retained %+v callable = %v, want %v", tc.retained, got, tc.wantTools)
		}
		if len(turn.Req.Tools) == 0 {
			t.Fatalf("retained %+v dropped the tool definitions", tc.retained)
		}
	}
}

// A coordinator's final prose turn keeps its tool definitions, which providers
// validate tool history against, and forbids calling them; it renders no tool
// procedures. A worker's final turn still offers its completion tool.
func TestProseFinishTurnForbidsCoordinatorToolUse(t *testing.T) {
	procedures := 0
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "read"}, {Name: "submit_verdict"}}},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ToolProcedures: func(context.Context, *api.Session, string, []string) (string, error) {
				procedures++
				return "procedure", nil
			},
		},
	})
	sess := &api.Session{ID: "closeout", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/repo", Posture: api.SessionPostureBuild}
	history := []api.Message{{Role: api.MessageRoleSystem, Content: "charter", ContextPinned: true}, {Role: api.MessageRoleUser, Content: "survey"}}
	st := &promptLoopTurnState{proseFinish: true}
	turn, err := loop.Model.buildTurnRequest(t.Context(), sess, sess.ID, history, prompts.CoordinatorProfileID, "survey", 3, 20, false, st)
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
			ToolProcedures: func(context.Context, *api.Session, string, []string) (string, error) { return "runner procedure", nil },
		},
	})
	history := []api.Message{{Role: api.MessageRoleSystem, Content: "charter", ContextPinned: true}}
	for range 50 {
		history = append(history, api.Message{Role: api.MessageRoleAssistant, Content: strings.Repeat("old context ", 100)})
	}
	history = append(history, api.Message{Role: api.MessageRoleUser, Content: "current request"})
	got, err := loop.Tools.appendToolProcedures(t.Context(), &api.Session{}, "coordinator", history, []tools.ToolMeta{{Name: "command"}})
	testutil.FailErr(t, "append procedures", err)
	fitted := compaction.DeterministicFit(compaction.CompactionConfig{KeepRecentMessages: 2}, compaction.ContextMessagesFromAPI(got), 100)
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
			ToolProcedures: func(context.Context, *api.Session, string, []string) (string, error) { return "", want },
		},
	})
	_, err := loop.Tools.appendToolProcedures(t.Context(), &api.Session{}, "coordinator", nil, []tools.ToolMeta{{Name: "command"}})
	if !errors.Is(err, want) {
		t.Fatalf("render failure = %v", err)
	}
}
