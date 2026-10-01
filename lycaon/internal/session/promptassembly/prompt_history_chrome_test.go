package promptassembly_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssemblePromptHistoryDropsTranscriptChromeBeforeDiet(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 8000
	msgs := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Redesign ibm.com for me. Propose 6 different options."},
		{
			ID: "warm", Role: api.MessageRoleSystem, Kind: api.MessageKindIndexWarming,
			IndexWarming: &api.IndexWarmingMeta{Trigger: "declared_url", Pages: 5, Topic: "ibm.com"},
			Content:      "Warmed web index — 5 hosts, 5 pages · Define 6 design directions for IBM.com redesign",
		},
		{ID: "pu", Role: api.MessageRoleSystem, Kind: api.MessageKindProgressUpdate},
		{ID: "bound", Role: api.MessageRoleSystem, Kind: api.MessageKindWorkflowBoundary},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "here are six options"},
	}
	out, report := promptassembly.Assemble(nil, msgs, promptassembly.Config{
		CompactionConfig: cfg,
	})
	if !containsStrategy(report.Strategies, "filter:prompt_history") {
		t.Fatalf("strategies=%v want filter:prompt_history", report.Strategies)
	}
	for _, msg := range out {
		if api.IsTranscriptChromeMessage(msg) {
			t.Fatalf("chrome survived assemble: id=%s kind=%s content=%q", msg.ID, msg.Kind, msg.Content)
		}
		if strings.Contains(msg.Content, "Warmed web index") {
			t.Fatalf("warm prose leaked into model history: %+v", msg)
		}
	}
	if len(out) != 2 {
		t.Fatalf("len=%d want user+assistant: %+v", len(out), out)
	}
}

func TestAssemblePromptHistoryDropsChromeRehydratedFromLossyCompactionView(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 8000
	canonical := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "research the site"},
		{
			ID: "warm", Role: api.MessageRoleSystem, Kind: api.MessageKindIndexWarming,
			IndexWarming: &api.IndexWarmingMeta{Trigger: "declared_url", Pages: 3},
			Content:      "Warmed web index — 1 host, 3 pages · research the site",
		},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "done"},
	}
	// Rehydrate a compacted view that omitted prompt-membership fields.
	deps := promptassembly.Config{
		CompactionConfig:      cfg,
		CompactionViewApplied: true,
	}
	history := func(history []api.Message) []api.Message {
		byID := make(map[string]api.Message, len(history))
		for _, msg := range history {
			byID[msg.ID] = msg
		}
		lossyWarm := api.Message{ID: "warm", Role: api.MessageRoleSystem, Content: canonical[1].Content}
		// Rehydration restores prompt-membership fields.
		lossyWarm = api.RehydrateTranscriptProjectionFields(lossyWarm, byID["warm"])
		return []api.Message{byID["u1"], lossyWarm, byID["a1"]}
	}(canonical)
	out, _ := promptassembly.Assemble(nil, history, deps)
	for _, msg := range out {
		if strings.Contains(msg.Content, "Warmed web index") || api.IsTranscriptChromeMessage(msg) {
			t.Fatalf("chrome leaked after lossy view rehydrate+assemble: %+v", msg)
		}
	}
}

func TestContextMessagesToAPIPreservesPromptMembership(t *testing.T) {
	original := []api.Message{{
		ID: "warm", Role: api.MessageRoleSystem, Kind: api.MessageKindIndexWarming,
		IndexWarming: &api.IndexWarmingMeta{Trigger: "search", Pages: 5},
		Content:      "Warmed web index — 5 hosts",
	}}
	ctxMsgs := compaction.ContextMessagesFromAPI(original)
	roundtrip := compaction.ContextMessagesToAPI(ctxMsgs, original)
	if len(roundtrip) != 1 {
		t.Fatalf("len=%d", len(roundtrip))
	}
	if roundtrip[0].Kind != api.MessageKindIndexWarming {
		t.Fatalf("kind=%q want index_warming", roundtrip[0].Kind)
	}
	if roundtrip[0].IndexWarming == nil || roundtrip[0].IndexWarming.Pages != 5 {
		t.Fatalf("index_warming meta lost: %+v", roundtrip[0].IndexWarming)
	}
	if api.IsPromptHistoryMessage(roundtrip[0]) {
		t.Fatal("roundtripped chrome must remain excluded from prompt history")
	}
}

// Withdrawn proposals leave unanswered tool calls that prompt assembly excludes.
func TestAssemblePromptHistoryClosesWithdrawnProposalToolCalls(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 8000
	msgs := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "run the four steps in order"},
		{
			ID: "withdrawn", Role: api.MessageRoleAssistant, Kind: api.MessageKindDraft,
			DraftStatus: api.DraftStatusWithdrawn,
			ToolCalls:   []api.ToolCall{{ID: "call_never_ran", Name: "command"}},
		},
		{
			ID: "steer", Role: api.MessageRoleUser, Kind: api.MessageKindUserContinuation,
			Content: "stop that - say ALPHA instead",
		},
		{
			ID: "a2", Role: api.MessageRoleAssistant, Content: "ALPHA",
			ToolCalls: []api.ToolCall{{ID: "call_answered", Name: "command"}},
		},
		{
			ID: "t2", Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{ToolCallID: "call_answered", AssistantMessageID: "a2"},
		},
	}
	out, _ := promptassembly.Assemble(nil, msgs, promptassembly.Config{
		CompactionConfig: cfg,
	})
	answered := map[string]bool{}
	for _, msg := range out {
		if msg.ID == "withdrawn" {
			t.Fatal("withdrawn proposal reached model history")
		}
		if msg.Role == api.MessageRoleTool && msg.ToolResult != nil {
			answered[msg.ToolResult.ToolCallID] = true
		}
	}
	for _, msg := range out {
		for _, tc := range msg.ToolCalls {
			if !answered[tc.ID] {
				t.Fatalf("tool call %q survived with no answering result: every provider rejects that", tc.ID)
			}
		}
	}
	if !containsMessageID(out, "steer") {
		t.Fatal("the steering continuation must survive - it is what the model has to act on")
	}
}

func containsMessageID(msgs []api.Message, id string) bool {
	for _, msg := range msgs {
		if msg.ID == id {
			return true
		}
	}
	return false
}
