package transcript_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolStepProsePreservesPrivateNotesAndReasoning(t *testing.T) {
	canonical := api.Message{ID: "step", Role: api.MessageRoleAssistant, Content: "The parser needs a negative-input test.", ToolCalls: []api.ToolCall{{ID: "call", Name: "read", Args: map[string]any{"path": "parser.go"}}}, ModelReasoning: &api.ModelReasoning{Text: "Check the sign handling first."}}
	lossy := canonical
	lossy.ModelReasoning = nil
	rehydrated := api.RehydrateTranscriptProjectionFields(lossy, canonical)
	projected := transcript.Project([]api.Message{rehydrated})
	got := projected[len(projected)-1]
	if got.Content != canonical.Content || !reflect.DeepEqual(got.ToolCalls, canonical.ToolCalls) || !reflect.DeepEqual(got.ModelReasoning, canonical.ModelReasoning) {
		t.Fatal("projection changed the note, native call, or reasoning")
	}
	if len(got.ContentParts) != 0 {
		t.Fatalf("projection inserted assistant content parts: %+v", got.ContentParts)
	}
	round := compaction.ContextMessagesToAPI(compaction.ContextMessagesFromAPI(projected), projected)
	afterFit := transcript.Project(round)
	last := afterFit[len(afterFit)-1]
	if last.Content != got.Content || !reflect.DeepEqual(last.ModelReasoning, got.ModelReasoning) {
		t.Fatal("context fitting changed the note or reasoning")
	}
	if canonical.Content != rehydrated.Content || len(canonical.ContentParts) != 0 {
		t.Fatal("projection changed canonical history")
	}
	if again := transcript.Project(projected); !reflect.DeepEqual(again, projected) {
		t.Fatal("repeated projection changed history")
	}
	if visible := messageview.TranscriptMessage(canonical); visible.Content != "" {
		t.Fatal("tool prose became user-visible")
	}
}

func TestAssistantPresentationUsesStructure(t *testing.T) {
	literal := "Internal working note, not shown to the user. Tool results record what executed and whether it succeeded.\n\n⟦D:model:internal_step_note⟧\n⟦D⟧This is quoted text.</think>"
	for _, tc := range []struct {
		name string
		message api.Message
		visible string
	}{
		{"tool step", api.Message{Role: api.MessageRoleAssistant, Content: literal, ToolCalls: []api.ToolCall{{ID: "call", Name: "read"}}}, ""},
		{"final answer", api.Message{Role: api.MessageRoleAssistant, Content: literal}, literal},
		{"public note", api.Message{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: literal}, literal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := transcript.Project([]api.Message{tc.message})
			got := out[len(out)-1]
			if got.Content != literal {
				t.Fatalf("model history changed literal content: %q", got.Content)
			}
			if visible := messageview.TranscriptMessage(tc.message); visible.Content != tc.visible {
				t.Fatalf("visible content = %q, want %q", visible.Content, tc.visible)
			}
		})
	}
}

func TestToolStepHistoryHasOneHostInstruction(t *testing.T) {
	messages := []api.Message{
		{Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, Content: "Follow the task."},
		{Role: api.MessageRoleAssistant, Content: "Inspect the parser.", ToolCalls: []api.ToolCall{{ID: "read-1", Name: "read"}}},
		{Role: api.MessageRoleTool, Content: "The parser accepts negative values."},
		{Role: api.MessageRoleAssistant, Content: "Check the assertion.", ToolCalls: []api.ToolCall{{ID: "read-2", Name: "read"}}},
	}
	out := transcript.Project(messages)
	if len(out) != len(messages) {
		t.Fatalf("projection changed message count: %d", len(out))
	}
	if strings.Count(out[0].Content, transcript.ContentAuthorityNotice()) != 1 || out[0].Role != api.MessageRoleSystem {
		t.Fatal("host instruction must appear once in the system message")
	}
	for _, i := range []int{1, 3} {
		if out[i].Content != messages[i].Content || out[i].Role != api.MessageRoleAssistant {
			t.Fatalf("tool step %d lost its native role or content: %+v", i, out[i])
		}
	}
	if again := transcript.Project(out); !reflect.DeepEqual(again, out) {
		t.Fatal("repeated projection changed history")
	}
}
