package history

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecordedRecallPageKeepsEnvelopeAndCursor(t *testing.T) {
	page := recall.HistoryPage{Format: "record", ThroughOrd: 1, Recorded: true,
		Entries: []recall.HistoryEntry{{MessageID: "original", Ord: 1, Content: strings.Repeat("😀", 4096), Runes: 9000, Offset: 4096}}}
	next := 8192
	page.NextOffset = &next
	raw, err := json.Marshal(map[string]any{"history": page})
	testutil.FailErr(t, "encode recall page", err)
	content := "[recall#4]\n" + string(raw) + "\nrecorded suffix"
	rows := projectRecordedToolBodies([]api.Message{{ID: "recall-result", Ord: 3, Role: api.MessageRoleTool,
		Content: content, ToolResult: &api.ToolResult{Tool: "recall", Content: content}}})
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(rows[0].Content)
	if !ok || prefix != "[recall#4]\n" || suffix != "\nrecorded suffix" || len(rows[0].Content) > 2048 {
		t.Fatalf("projection lost the tool envelope or exceeded its budget: %q", rows[0].Content)
	}
	var projected struct {
		History recall.HistoryPage `json:"history"`
	}
	testutil.FailErr(t, "decode projected recall page", json.Unmarshal([]byte(body), &projected))
	if len(projected.History.Entries) != 1 || projected.History.NextOffset == nil {
		t.Fatal("projection lost its recorded page")
	}
	entry := projected.History.Entries[0]
	if *projected.History.NextOffset != entry.Offset+utf8.RuneCountInString(entry.Content) {
		t.Fatal("projection skipped recorded characters")
	}
}
