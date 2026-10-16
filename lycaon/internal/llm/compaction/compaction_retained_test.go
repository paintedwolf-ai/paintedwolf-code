package compaction

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestContextRetentionKeepsRecentToolBatchBeforeSystemTail(t *testing.T) {
	rows := append(fitRows(100), ContextMessage{
		ID: "call", Role: "assistant", ToolCalls: []api.ToolCall{{ID: "a", Name: "read"}, {ID: "b", Name: "read"}},
	}, ContextMessage{ID: "a", Role: "tool", ToolCallID: "a", Content: "first file"},
		ContextMessage{ID: "b", Role: "tool", ToolCallID: "b", Content: "second file"})
	for range 5 {
		rows = append(rows, ContextMessage{Role: "system", Content: "current host state"})
	}
	for name, retained := range map[string][]ContextMessage{
		"fit":     DeterministicFit(CompactionConfig{KeepRecentMessages: 2}, rows, 1),
		"compact": retainedCompactionMessages(rows, 2),
	} {
		t.Run(name, func(t *testing.T) {
			var ids []string
			for _, row := range retained {
				if row.ID == "call" || row.ID == "a" || row.ID == "b" {
					ids = append(ids, row.ID)
				}
			}
			if !slices.Equal(ids, []string{"call", "a", "b"}) {
				t.Fatalf("retained batch = %v, want producing call and both results", ids)
			}
			if len(retained) >= len(rows) {
				t.Fatal("retention did not remove old history")
			}
		})
	}
}

func TestRecentRetentionUsesAvailableWorkingBudget(t *testing.T) {
	rows := []ContextMessage{
		{ID: "request", Role: "user", Content: "Compare the original observation to the current source", ContextPinned: true},
		{ID: "recover", Role: "assistant", ToolCalls: []api.ToolCall{{ID: "r", Name: "recall"}}, ModelReasoning: &api.ModelReasoning{Text: strings.Repeat("recover recorded evidence ", 20)}},
		{ID: "observation", Role: "tool", ToolCallID: "r", Content: strings.Repeat("original checksum cypress ", 40)},
		{ID: "read", Role: "assistant", ToolCalls: []api.ToolCall{{ID: "f", Name: "read"}}},
		{ID: "rejection", Role: "tool", ToolCallID: "f", Content: "Rejected: correct the argument type and retry"},
	}
	cfg := CompactionConfig{ModelContextWindow: 16384, KeepRecentMessages: 4}
	budget := EstimateMessagesTokens(rows)
	count := recentCompactionCount(cfg, rows, budget)
	kept := retainedCompactionMessages(rows, count)
	if len(kept) != len(rows) {
		t.Fatalf("working exchanges fit %d tokens but retained %d of %d rows", budget, len(kept), len(rows))
	}
	tight := EstimateMessagesTokens([]ContextMessage{rows[0], rows[3], rows[4]})
	kept = retainedCompactionMessages(rows, recentCompactionCount(cfg, rows, tight))
	if len(kept) != 3 || kept[0].ID != "request" || kept[1].ID != "read" || kept[2].ID != "rejection" {
		t.Fatalf("tight budget did not preserve the complete latest exchange and pinned request: %+v", kept)
	}
	if EstimateMessagesTokens(kept) > tight {
		t.Fatal("retention exceeded the available working budget")
	}
}
