package api

import "testing"

func TestFilterPromptHistoryDropsBoundariesKeepsInternalUserKicks(t *testing.T) {
	in := []Message{
		{ID: "b1", Role: MessageRoleSystem, Kind: MessageKindWorkflowBoundary, Content: ""},
		{ID: "i1", Role: MessageRoleUser, Content: "kick", Visibility: MessageVisibilityInternal},
		{ID: "u1", Role: MessageRoleUser, Content: "build game", Visibility: MessageVisibilityTranscript},
		{ID: "a1", Role: MessageRoleAssistant, Content: "ok"},
	}
	out := FilterPromptHistory(in)
	if len(out) != 3 {
		t.Fatalf("len = %d want 3: %+v", len(out), out)
	}
	if out[0].ID != "i1" || out[1].ID != "u1" || out[2].ID != "a1" {
		t.Fatalf("ids = %v", []string{out[0].ID, out[1].ID, out[2].ID})
	}
}

func TestFilterPromptHistoryDropsAllTranscriptChrome(t *testing.T) {
	in := []Message{
		{ID: "u1", Role: MessageRoleUser, Content: "redesign ibm.com"},
		{ID: "warm", Role: MessageRoleSystem, Kind: MessageKindIndexWarming, Content: "Warmed web index — 5 hosts, 5 pages · Define directions"},
		{ID: "warm-meta", Role: MessageRoleSystem, IndexWarming: &IndexWarmingMeta{Trigger: "search", Pages: 2}, Content: "Warmed web index — 2 pages"},
		{ID: "pu", Role: MessageRoleSystem, Kind: MessageKindProgressUpdate},
		{ID: "pc", Role: MessageRoleSystem, ProgressComplete: &ProgressCompleteMeta{}},
		{ID: "fb", Role: MessageRoleSystem, WorkflowFeedback: &WorkflowFeedbackMeta{Prompt: "pick one"}},
		{ID: "explain", Role: MessageRoleSystem, Kind: MessageKindWorkflowExplain, Content: "A full scan runs first"},
		{ID: "explain-meta", Role: MessageRoleSystem, WorkflowExplain: &WorkflowExplainMeta{PhaseID: "ingest"}},
		{ID: "plan", Role: MessageRoleSystem, Kind: MessageKindBlueprint, Blueprint: &BlueprintMeta{}},
		{ID: "bound", Role: MessageRoleSystem, WorkflowBoundary: &WorkflowBoundaryMeta{}},
		{ID: "a1", Role: MessageRoleAssistant, Content: "here are options"},
	}
	out := FilterPromptHistory(in)
	if len(out) != 2 {
		t.Fatalf("len = %d want 2 (user+assistant): %+v", len(out), out)
	}
	if out[0].ID != "u1" || out[1].ID != "a1" {
		t.Fatalf("ids = %v want u1,a1", []string{out[0].ID, out[1].ID})
	}
}

func TestRehydrateTranscriptProjectionFields(t *testing.T) {
	canonical := Message{
		ID: "warm", Role: MessageRoleSystem, Kind: MessageKindIndexWarming,
		IndexWarming: &IndexWarmingMeta{Trigger: "declared_url", Pages: 3},
		Content:      "Warmed web index — 1 host, 3 pages · topic",
	}
	lossy := Message{ID: "warm", Role: MessageRoleSystem, Content: canonical.Content}
	got := RehydrateTranscriptProjectionFields(lossy, canonical)
	if !IsIndexWarmingMessage(got) || !IsTranscriptChromeMessage(got) {
		t.Fatalf("restored = %+v want index_warming chrome", got)
	}
	if IsPromptHistoryMessage(got) {
		t.Fatal("restored chrome must stay outside prompt history")
	}
}

func TestRehydrateTranscriptProjectionFieldsPreservesDraftLifecycle(t *testing.T) {
	canonical := Message{
		ID: "draft", Role: MessageRoleAssistant, Kind: MessageKindDraft,
		Visibility: MessageVisibilityTranscript, DraftStatus: DraftStatusCommitted,
		DraftVersionCount: 3, WorkflowRunID: "run-1", Ord: 7, Seq: 11,
	}
	got := RehydrateTranscriptProjectionFields(
		Message{ID: "draft", Role: MessageRoleAssistant, Content: "compacted"},
		canonical,
	)
	if got.Kind != MessageKindDraft || got.DraftStatus != DraftStatusCommitted ||
		got.DraftVersionCount != 3 || got.WorkflowRunID != "run-1" ||
		got.Ord != 7 || got.Seq != 11 {
		t.Fatalf("rehydrated draft = %+v", got)
	}
}

func TestEnsureToolCallGroupContiguityForPromptMovesInternalNudgeAfterTools(t *testing.T) {
	in := []Message{
		{ID: "a1", Role: MessageRoleAssistant, ToolCalls: []ToolCall{
			{ID: "call_a", Name: "grep"},
			{ID: "call_b", Name: "read"},
		}},
		{ID: "n1", Role: MessageRoleUser, Content: "[host:coordinator-citation-grounding] Rejected", Visibility: MessageVisibilityInternal},
		{ID: "t1", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "call_a", Tool: "grep"}, Content: "grep ok"},
		{ID: "t2", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "call_b", Tool: "read"}, Content: "read ok"},
	}
	out := EnsureToolCallGroupContiguityForPrompt(in)
	if len(out) != 4 {
		t.Fatalf("len = %d want 4: %+v", len(out), out)
	}
	if out[0].ID != "a1" || out[1].ID != "t1" || out[2].ID != "t2" || out[3].ID != "n1" {
		t.Fatalf("order = %v want a1,t1,t2,n1", []string{out[0].ID, out[1].ID, out[2].ID, out[3].ID})
	}
}

func TestEnsureToolCallGroupContiguityForPromptMovesAgentNoteAfterSiblingTools(t *testing.T) {
	in := []Message{
		{ID: "a1", Role: MessageRoleAssistant, ToolCalls: []ToolCall{
			{ID: "call_note", Name: "surface_note"},
			{ID: "call_task", Name: "task"},
		}},
		{ID: "t-note", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "call_note", Tool: "surface_note"}, Content: "noted"},
		{ID: "note", Role: MessageRoleAssistant, Kind: MessageKindAgentNote, Visibility: MessageVisibilityTranscript, Content: "A verified milestone."},
		{ID: "t-task", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "call_task", Tool: "task"}, Content: "enqueued"},
	}
	out := EnsureToolCallGroupContiguityForPrompt(in)
	if len(out) != 4 {
		t.Fatalf("len = %d want 4: %+v", len(out), out)
	}
	if out[0].ID != "a1" || out[1].ID != "t-note" || out[2].ID != "t-task" || out[3].ID != "note" {
		t.Fatalf("order = %v want a1,t-note,t-task,note", []string{out[0].ID, out[1].ID, out[2].ID, out[3].ID})
	}
}

func TestEnsureToolCallGroupContiguityForPromptDoesNotSettleOnUnmatchedResult(t *testing.T) {
	in := []Message{
		{ID: "a1", Role: MessageRoleAssistant, ToolCalls: []ToolCall{
			{ID: "call_a", Name: "grep"},
			{ID: "call_b", Name: "read"},
		}},
		{ID: "orphan", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "other", Tool: "read"}},
		{ID: "note", Role: MessageRoleAssistant, Kind: MessageKindAgentNote, Visibility: MessageVisibilityTranscript},
		{ID: "t1", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "call_a", Tool: "grep"}},
		{ID: "t2", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "call_b", Tool: "read"}},
	}
	out := EnsureToolCallGroupContiguityForPrompt(in)
	if len(out) != 5 {
		t.Fatalf("len = %d want 5: %+v", len(out), out)
	}
	if out[0].ID != "a1" || out[1].ID != "orphan" || out[2].ID != "t1" || out[3].ID != "t2" || out[4].ID != "note" {
		t.Fatalf("order = %v want a1,orphan,t1,t2,note", []string{out[0].ID, out[1].ID, out[2].ID, out[3].ID, out[4].ID})
	}
}

func TestSpanScrollAnchorPrefersUserSlash(t *testing.T) {
	boundary := Message{ID: "b1", Kind: MessageKindWorkflowBoundary, Visibility: MessageVisibilityTranscript}
	appended := []Message{
		{ID: "u1", Role: MessageRoleUser, Content: "/plan"},
		boundary,
	}
	if got := SpanScrollAnchor(appended, boundary); got != "u1" {
		t.Fatalf("anchor = %q want u1", got)
	}
}

func TestSpanScrollAnchorAmbientEmpty(t *testing.T) {
	boundary := Message{ID: "b1", Kind: MessageKindWorkflowBoundary, Visibility: MessageVisibilityInternal}
	if got := SpanScrollAnchor([]Message{boundary}, boundary); got != "" {
		t.Fatalf("anchor = %q want empty for ambient", got)
	}
}

func TestSpanScrollAnchorCatalogBoundaryFallback(t *testing.T) {
	boundary := Message{ID: "b1", Kind: MessageKindWorkflowBoundary, Visibility: MessageVisibilityTranscript}
	if got := SpanScrollAnchor([]Message{boundary}, boundary); got != "b1" {
		t.Fatalf("anchor = %q want b1", got)
	}
}

func TestFilterPromptHistoryDropsWithdrawnProposalAndItsUnansweredCalls(t *testing.T) {
	// The shape a queue Send leaves behind: the proposal is withdrawn before its tool
	// calls run, so nothing ever answers them. Replaying the row poisons every later
	// turn, because the provider rejects a tool-call group no result closes.
	in := []Message{
		{ID: "u1", Role: MessageRoleUser, Content: "count to four"},
		{
			ID: "a-withdrawn", Role: MessageRoleAssistant, Kind: MessageKindDraft,
			DraftStatus: DraftStatusWithdrawn,
			ToolCalls:   []ToolCall{{ID: "call_dead", Name: "command"}},
		},
		{ID: "u2", Role: MessageRoleUser, Kind: MessageKindUserContinuation, Content: "stop, say ALPHA"},
		{
			ID: "a-live", Role: MessageRoleAssistant, Content: "ALPHA",
			ToolCalls: []ToolCall{{ID: "call_live", Name: "command"}},
		},
		{ID: "t1", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "call_live"}},
	}
	out := FilterPromptHistory(in)
	for _, msg := range out {
		if msg.ID == "a-withdrawn" {
			t.Fatal("withdrawn proposal reached model history")
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID == "call_dead" {
				t.Fatal("unanswered tool call reached model history")
			}
		}
	}
	if len(out) != 4 {
		t.Fatalf("len = %d want 4: %+v", len(out), out)
	}
}

func TestFilterPromptHistoryKeepsWithdrawnRowOutOfHistoryButNotTheTranscript(t *testing.T) {
	withdrawn := Message{
		ID: "a1", Role: MessageRoleAssistant, Kind: MessageKindDraft,
		DraftStatus: DraftStatusWithdrawn, Content: "half a thought",
	}
	if !IsRetractedAttemptMessage(withdrawn) {
		t.Fatal("withdrawn draft must classify as a retracted attempt")
	}
	if IsPromptHistoryMessage(withdrawn) {
		t.Fatal("withdrawn draft must stay out of prompt history")
	}
	if IsTranscriptChromeMessage(withdrawn) {
		t.Fatal("a retracted attempt is not transcript chrome — it stays visible as audit")
	}
}

func TestIsPromptHistoryMessageKeepsCommittedAndExcludesRejectedDrafts(t *testing.T) {
	committed := Message{
		ID: "a1", Role: MessageRoleAssistant, Kind: MessageKindDraft,
		DraftStatus: DraftStatusCommitted, Content: "step one done",
	}
	if !IsPromptHistoryMessage(committed) {
		t.Fatal("a committed draft is canonical model history")
	}
	// A rejected coordinator attempt is retracted: host guidance nudges teach the model.
	rejected := Message{
		ID: "a2", Role: MessageRoleAssistant, DraftStatus: DraftStatusRejected, Content: "refused body",
	}
	if IsPromptHistoryMessage(rejected) {
		t.Fatal("a rejected attempt is retracted and must stay out of prompt history")
	}
}

func TestFilterPromptHistoryDropsSupersededReport(t *testing.T) {
	in := []Message{
		{ID: "u1", Role: MessageRoleUser, Content: "ship it"},
		{ID: "old", Role: MessageRoleAssistant, Kind: MessageKindSuperseded, Content: "ungrounded claim"},
		{ID: "new", Role: MessageRoleAssistant, Content: "grounded claim"},
	}
	out := FilterPromptHistory(in)
	if len(out) != 2 || out[1].ID != "new" {
		t.Fatalf("out = %+v want u1,new", out)
	}
}

func TestCloseToolCallGroupsForPromptKeepsPartiallyAnsweredGroups(t *testing.T) {
	in := []Message{
		{
			ID: "a1", Role: MessageRoleAssistant, Content: "working",
			ToolCalls: []ToolCall{{ID: "answered"}, {ID: "orphan"}},
		},
		{ID: "t1", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "answered"}},
	}
	out := CloseToolCallGroupsForPrompt(in)
	if len(out) != 2 {
		t.Fatalf("len = %d want 2: %+v", len(out), out)
	}
	if len(out[0].ToolCalls) != 1 || out[0].ToolCalls[0].ID != "answered" {
		t.Fatalf("tool calls = %+v want only the answered one", out[0].ToolCalls)
	}
	if out[0].Content != "working" {
		t.Fatalf("content = %q want the prose kept", out[0].Content)
	}
}

func TestCloseToolCallGroupsForPromptLeavesToolRowsAlone(t *testing.T) {
	// A standalone host event row carries no tool call id, and a guard-reject refusal
	// may outlive the assistant row that proposed it. Neither may be dropped here.
	in := []Message{
		{ID: "host", Role: MessageRoleTool, ToolResult: &ToolResult{Content: "overlay event"}},
		{ID: "refusal", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "gone"}},
	}
	out := CloseToolCallGroupsForPrompt(in)
	if len(out) != 2 {
		t.Fatalf("len = %d want 2: %+v", len(out), out)
	}
}

func TestCloseToolCallGroupsForPromptIsAStableNoOpWhenEveryCallIsAnswered(t *testing.T) {
	in := []Message{
		{ID: "a1", Role: MessageRoleAssistant, ToolCalls: []ToolCall{{ID: "c1"}, {ID: "c2"}}},
		{ID: "t1", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "c1"}},
		{ID: "t2", Role: MessageRoleTool, ToolResult: &ToolResult{ToolCallID: "c2"}},
	}
	out := CloseToolCallGroupsForPrompt(in)
	if len(out) != 3 || len(out[0].ToolCalls) != 2 {
		t.Fatalf("out = %+v want the input unchanged", out)
	}
}
