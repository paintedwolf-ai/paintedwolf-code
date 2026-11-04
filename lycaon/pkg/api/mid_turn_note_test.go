package api

import "testing"

func TestMidTurnNoteIDIsDeterministicPerCarrier(t *testing.T) {
	t.Parallel()
	if MidTurnNoteID("a") != MidTurnNoteID(" a ") || MidTurnNoteID("a") == MidTurnNoteID("b") {
		t.Fatal("note id must derive from the carrier alone")
	}
}

// AC-MID-11, AC-MID-17: the model reads published text once, on its carrier.
func TestPromptHistoryProjectsPublishedTextOnItsCarrier(t *testing.T) {
	t.Parallel()
	carrier := Message{ID: "c1", Role: MessageRoleAssistant, Content: "Checking [read#1:2].", ToolCalls: []ToolCall{{ID: "t1", Name: "read"}}}
	derived := Message{ID: MidTurnNoteID("c1"), Role: MessageRoleAssistant, Kind: MessageKindAgentNote, Content: "Checking."}
	result := Message{ID: "r1", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "t1"}}
	toolNote := Message{ID: "old-note", Role: MessageRoleAssistant, Kind: MessageKindAgentNote, Content: "From surface_note."}
	got := FilterPromptHistory([]Message{carrier, derived, result, toolNote})
	if len(got) != 3 || got[0].ID != "c1" || got[1].ID != "r1" || got[2].ID != "old-note" {
		t.Fatalf("prompt history = %+v", got)
	}
	// With its carrier compacted away, the note is the only copy left.
	if got := FilterPromptHistory([]Message{derived, result}); len(got) != 2 || got[0].ID != derived.ID {
		t.Fatalf("orphaned note dropped: %+v", got)
	}
}
