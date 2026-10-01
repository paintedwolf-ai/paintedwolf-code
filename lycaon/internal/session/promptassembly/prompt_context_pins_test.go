package promptassembly_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/pkg/api"
)

func workerSession() *api.Session {
	return &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "security-reviewer"}
}

func charterMessage() api.Message {
	return api.Message{
		ID:   "charter",
		Role: api.MessageRoleUser,
		Content: guidance.MarkerWorkerTaskPreamble + "\nTask mode:\n- mode: read\n\n" +
			guidance.MarkerWorkerTaskAssignment + "\nLeg assignment (security-reviewer): review egress controls.",
	}
}

// workerHistory is a charter followed by many rounds of tool traffic.
func workerHistory(rounds int) []api.Message {
	out := []api.Message{charterMessage()}
	for i := 0; i < rounds; i++ {
		out = append(out,
			api.Message{
				ID:        "a" + string(rune('a'+i%26)) + strings.Repeat("x", i%3),
				Role:      api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{{ID: "call", Name: "grep", Args: map[string]any{"pattern": "Foo"}}},
			},
			api.Message{
				ID:         "t" + string(rune('a'+i%26)) + strings.Repeat("y", i%3),
				Role:       api.MessageRoleTool,
				Content:    strings.Repeat("match evidence ", 60),
				ToolResult: &api.ToolResult{Tool: "grep", ToolCallID: "call"},
			},
		)
	}
	return out
}

func assemblyDeps(ceiling int) promptassembly.Config {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = ceiling
	return promptassembly.Config{CompactionConfig: cfg}
}

func containsCharter(msgs []api.Message) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, guidance.MarkerWorkerTaskAssignment) {
			return true
		}
	}
	return false
}

// A worker leg that outruns the context window keeps its assignment. The charter
// sits with the oldest rows, which is exactly what the fit drops first.
func TestAssemblePromptHistoryKeepsWorkerCharterUnderPressure(t *testing.T) {
	history := workerHistory(200)
	for _, ceiling := range []int{40000, 8000, 2000, 500} {
		out, report := promptassembly.Assemble(workerSession(), history, assemblyDeps(ceiling))
		if !containsCharter(out) {
			t.Fatalf("ceiling %d: worker lost its charter across %d retained rows (strategies=%v)",
				ceiling, len(out), report.Strategies)
		}
	}
}

// The trim has to be visible to the model, or a missing fact reads as a fact
// worth re-fetching.
func TestAssemblePromptHistoryStatesTheTrim(t *testing.T) {
	out, _ := promptassembly.Assemble(workerSession(), workerHistory(200), assemblyDeps(2000))
	notices := 0
	for _, m := range out {
		if m.Content == guidance.ContextTrimNotice {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("want one context-trim notice in the assembled prompt, got %d of %d rows", notices, len(out))
	}
}

func TestMarkContextPinsPreservesLatestRootRequest(t *testing.T) {
	root := &api.Session{ID: "root"}
	history := []api.Message{
		{ID: "old", Role: api.MessageRoleUser, Content: "old request"},
		{ID: "current", Role: api.MessageRoleUser, Content: "current request"},
		{ID: "kick", Role: api.MessageRoleUser, Content: "host continuation", Visibility: api.MessageVisibilityInternal},
	}
	out := promptassembly.MarkContextPins(root, history)
	if out[0].ContextPinned || !out[1].ContextPinned || out[2].ContextPinned {
		t.Fatal("did not pin only the current visible request")
	}
	if history[1].ContextPinned {
		t.Fatal("pinning mutated canonical history")
	}
	worker := promptassembly.MarkContextPins(workerSession(), []api.Message{charterMessage()})
	if !worker[0].ContextPinned {
		t.Fatal("worker charter row was not pinned")
	}
}

func TestAssemblePromptHistoryKeepsRootRequestUnderPressure(t *testing.T) {
	history := workerHistory(200)
	history[0] = api.Message{ID: "request", Role: api.MessageRoleUser, Content: "Build the service report CLI"}
	out, _ := promptassembly.Assemble(&api.Session{ID: "root"}, history, assemblyDeps(500))
	for _, msg := range out {
		if msg.ID == "request" && msg.Content == history[0].Content && msg.ContextPinned {
			return
		}
	}
	t.Fatal("root request disappeared under context pressure")
}

// Hoisting tool-linked rows would break their call/result ordering.
func TestMarkContextPinsRefusesToolLinkedRows(t *testing.T) {
	toolRow := api.Message{
		Role:       api.MessageRoleUser,
		Content:    guidance.MarkerWorkerTaskAssignment + " embedded in a tool result",
		ToolResult: &api.ToolResult{Tool: "read"},
	}
	out := promptassembly.MarkContextPins(workerSession(), []api.Message{toolRow})
	if out[0].ContextPinned {
		t.Fatal("pinned a tool-linked row; hoisting it would orphan its tool_call")
	}
}

func TestMarkContextPinsDoesNotMutateInput(t *testing.T) {
	history := []api.Message{charterMessage()}
	_ = promptassembly.MarkContextPins(workerSession(), history)
	if history[0].ContextPinned {
		t.Fatal("MarkContextPins mutated the caller's history")
	}
}
