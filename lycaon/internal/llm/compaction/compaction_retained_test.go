package compaction

import (
	"slices"
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
