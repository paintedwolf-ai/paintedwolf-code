package assembly

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSkillPreloadInjectUsesRenderedProcedureWithoutRoster(t *testing.T) {
	e := &AssemblyEngine{}
	selected := &turnload.SkillPreload{Name: "selected-skill", Score: 3.6, Body: "Run the targeted check."}
	e.SetDeps(AssemblyDeps{Injects: promptstest.InjectRenderer(t), SkillPreload: func(string) *turnload.SkillPreload { return selected }})
	for i := 0; i < 2; i++ {
		message, ok := testTurnContext(e).skillProcedureInject(t.Context(), &api.Session{ID: "s"}, "coordinator")
		if !ok || !strings.Contains(message.Content, selected.Body) || !strings.Contains(message.Content, "### Procedure: selected-skill") || strings.Contains(message.Content, "skills_read") {
			t.Fatalf("procedure=%+v present=%v", message, ok)
		}
		if message.ContextPinned || message.Authority != api.ContentAuthoritySystem {
			t.Fatalf("procedure envelope=%+v", message)
		}
	}
	selected.Resources = []string{"references/guide.md"}
	if message, ok := testTurnContext(e).skillProcedureInject(t.Context(), &api.Session{ID: "s"}, "coordinator"); !ok || !strings.Contains(message.Content, `"need":"selected-skill"`) || !strings.Contains(message.Content, "references/guide.md") {
		t.Fatalf("procedure with resources=%+v present=%v", message, ok)
	}
	selected = nil
	if _, ok := testTurnContext(e).skillProcedureInject(t.Context(), &api.Session{ID: "s"}, "coordinator"); ok {
		t.Fatal("injected an unselected skill")
	}
}

func TestInterleaveSkillGuidanceAnchorsInHistoryPrefix(t *testing.T) {
	e := &AssemblyEngine{}
	selected := &turnload.SkillPreload{Name: "game-ai", Score: 3.8, Body: "Use minimax."}
	e.SetDeps(AssemblyDeps{Injects: promptstest.InjectRenderer(t), SkillPreload: func(string) *turnload.SkillPreload { return selected }})
	sess := &api.Session{ID: "s"}

	u1 := api.Message{ID: "u1", Ord: 1, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "Build tic-tac-toe"}
	a1 := api.Message{ID: "a1", Ord: 2, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "write"}}}
	t1 := api.Message{ID: "t1", Ord: 3, Role: api.MessageRoleTool, Content: "ok", ToolResult: &api.ToolResult{ToolCallID: "c1", Tool: "write", Content: "ok"}}
	a2 := api.Message{ID: "a2", Ord: 4, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c2", Name: "command"}}}
	t2 := api.Message{ID: "t2", Ord: 5, Role: api.MessageRoleTool, Content: "ok", ToolResult: &api.ToolResult{ToolCallID: "c2", Tool: "command", Content: "ok"}}

	// Turn-open preload stays anchored immediately after u1 across iterations.
	gotTurnOpen := testTurnContext(e).interleaveSkillGuidance(t.Context(), sess, "coordinator", []api.Message{u1, a1, t1, a2, t2})
	if len(gotTurnOpen) != 6 || gotTurnOpen[0].ID != "u1" || gotTurnOpen[1].ID != "host-skill-procedure" || gotTurnOpen[2].ID != "a1" {
		t.Fatalf("turn-open preload not anchored after opener: %+v", gotTurnOpen)
	}

	// Tool-event preload anchors immediately after the triggering tool batch's results.
	selected.Tool = "command"
	gotToolEvent := testTurnContext(e).interleaveSkillGuidance(t.Context(), sess, "coordinator", []api.Message{u1, a1, t1, a2, t2})
	if len(gotToolEvent) != 6 || gotToolEvent[4].ID != "t2" || gotToolEvent[5].ID != "host-skill-procedure" {
		t.Fatalf("tool-event preload not anchored after matching tool batch: %+v", gotToolEvent)
	}

	// Once the turn's opening prefix is compacted or omitted after u1, turn-open preload is not re-injected.
	selected.Tool = ""
	omission := api.Message{ID: "host-history-coverage", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Content: "Messages omitted"}
	gotCompacted := testTurnContext(e).interleaveSkillGuidance(t.Context(), sess, "coordinator", []api.Message{u1, omission, a2, t2})
	if len(gotCompacted) != 4 {
		t.Fatalf("expected turn-open preload to omit after turn prefix compaction/omission: %+v", gotCompacted)
	}
}
