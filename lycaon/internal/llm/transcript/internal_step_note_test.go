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
	if !strings.Contains(got.Content, "Internal working note") || !strings.Contains(got.Content, "not shown to the user") {
		t.Fatalf("tool prose is unmarked public speech: %q", got.Content)
	}
	if !strings.Contains(got.Content, canonical.Content) || !reflect.DeepEqual(got.ToolCalls, canonical.ToolCalls) || !reflect.DeepEqual(got.ModelReasoning, canonical.ModelReasoning) {
		t.Fatal("projection lost the note, native call, or reasoning")
	}
	if len(got.ContentParts) == 0 || got.ContentParts[0].Authority != api.ContentAuthorityNone || got.ContentParts[0].Origin != api.MessageOriginHost {
		t.Fatal("visibility frame gained instruction authority")
	}
	round := compaction.ContextMessagesToAPI(compaction.ContextMessagesFromAPI(projected), projected)
	afterFit := transcript.Project(round)
	last := afterFit[len(afterFit)-1]
	if last.Content != got.Content || !reflect.DeepEqual(last.ModelReasoning, got.ModelReasoning) {
		t.Fatal("context fitting changed the note frame or reasoning")
	}
	if canonical.Content != rehydrated.Content || len(canonical.ContentParts) != 0 {
		t.Fatal("projection changed canonical history")
	}
	if again := transcript.Project(projected); !reflect.DeepEqual(again, projected) {
		t.Fatal("repeated projection changed the frame")
	}
	if visible := messageview.TranscriptMessage(canonical); visible.Content != "" {
		t.Fatal("tool prose became user-visible")
	}
}

func TestToolStepFrameDoesNotLabelPublicSpeechOrEmptySteps(t *testing.T) {
	for _, msg := range []api.Message{
		{Role: api.MessageRoleAssistant, Content: "Finished."},
		{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Tests pass."},
		{Role: api.MessageRoleAssistant, Content: "  ", ToolCalls: []api.ToolCall{{ID: "call", Name: "read"}}},
		{Role: api.MessageRoleUser, Content: "Check it."},
	} {
		out := transcript.Project([]api.Message{msg})
		if strings.Contains(out[len(out)-1].Content, "Internal working note") {
			t.Fatalf("incorrect internal frame: %+v", msg)
		}
	}
}
