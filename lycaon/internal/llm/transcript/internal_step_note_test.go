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
	if len(got.ContentParts) != 2 ||
		got.ContentParts[0].Origin != api.MessageOriginHost ||
		got.ContentParts[0].Authority != api.ContentAuthoritySystem ||
		got.ContentParts[0].TrustTier != api.ContentTrustTierTrusted ||
		got.ContentParts[0].Source != "internal_step_note" {
		t.Fatalf("host notice lost system authority: %+v", got.ContentParts)
	}
	if got.ContentParts[1].Origin != api.MessageOriginModel ||
		got.ContentParts[1].Authority != api.ContentAuthorityNone ||
		got.ContentParts[1].TrustTier != api.ContentTrustTierUntrusted ||
		got.ContentParts[1].Source != "internal_step_note" {
		t.Fatalf("model working note not tagged as untrusted data: %+v", got.ContentParts)
	}
	if strings.Contains(got.Content, "⟦D:host:internal_step_note") || strings.Contains(got.Content, "⟦D⟧Internal working note") {
		t.Fatalf("host notice was data-marked: %q", got.Content)
	}
	if !strings.Contains(got.Content, "⟦D:model:internal_step_note⟧\n⟦D⟧"+canonical.Content) {
		t.Fatalf("model working note missing data boundary: %q", got.Content)
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

func TestToolStepNoteStripsParrotedMarkersWithoutDoubleStacking(t *testing.T) {
	call := []api.ToolCall{{ID: "call", Name: "read", Args: map[string]any{"path": "parser.go"}}}
	for _, raw := range []string{
		"⟦D:host:internal_step_note⟧\n⟦D⟧Internal working note, not shown to the user. Tool results record what executed and whether it succeeded.\n\n⟦D:host:internal_step_note⟦\n⟦D⟧Internal working note, not shown to the user. Tool results record what executed and whether it succeeded.\n\nCheck the sign handling.\nVerify bounds.",
		"⟦D:model:internal_step_note⟧\n⟦D⟧Check the sign handling.\n⟦D⟧Verify bounds.",
		"⟦D:host:internal_step_note⟧\n⟦D⟧Internal work note from a tool step; not shown to the user. Still send the final user-visible reply.\n\nCheck the sign handling.\nVerify bounds.",
		"Internal work note from a tool step; not shown to the user. Still send the final user-visible reply.\n\nCheck the sign handling.\nVerify bounds.",
	} {
		out := transcript.Project([]api.Message{{Role: api.MessageRoleAssistant, Content: raw, ToolCalls: call}})
		got := out[len(out)-1]
		if strings.Contains(got.Content, "⟦D:host:internal_step_note") || strings.Contains(got.Content, "⟦D⟧⟦D⟧") {
			t.Fatalf("parroted markers leaked or stacked: %q", got.Content)
		}
		if strings.Count(got.Content, "Internal working note") != 1 || strings.Count(got.Content, "⟦D:model:internal_step_note⟧") != 1 {
			t.Fatalf("unexpected note framing count: %q", got.Content)
		}
		wantBody := "⟦D:model:internal_step_note⟧\n⟦D⟧Check the sign handling.\n⟦D⟧Verify bounds."
		if !strings.HasSuffix(got.Content, wantBody) {
			t.Fatalf("got %q, want suffix %q", got.Content, wantBody)
		}
		unprojected := got
		unprojected.ModelProjected = false
		reprojected := transcript.Project([]api.Message{unprojected})
		if reprojected[len(reprojected)-1].Content != got.Content {
			t.Fatalf("reprojection after clearing ModelProjected changed content: %q", reprojected[len(reprojected)-1].Content)
		}
	}

	empty := transcript.Project([]api.Message{{
		Role:      api.MessageRoleAssistant,
		Content:   "⟦D:host:internal_step_note⟧\n⟦D⟧Internal working note, not shown to the user. Tool results record what executed and whether it succeeded.\n",
		ToolCalls: call,
	}})
	if got := empty[len(empty)-1]; got.Content != "" || len(got.ContentParts) != 0 {
		t.Fatalf("header-only tool step was not cleared: %+v", got)
	}
}

func TestFinalAssistantTurnStripsParrotedInternalStepNoteMarkers(t *testing.T) {
	for _, raw := range []string{
		"⟦D:host:internal_step_note⟧\n⟦D⟧Internal working note, not shown to the user. Tool results record what executed and whether it succeeded.\n\nFinished the implementation.",
		"⟦D:model:internal_step_note⟧\n⟦D⟧Finished the implementation.",
		"⟦D:host:internal_step_note⟧\n⟦D⟧Internal work note from a tool step; not shown to the user. Still send the final user-visible reply.\n\nFinished the implementation.",
		"Internal work note from a tool step; not shown to the user. Still send the final user-visible reply.\n\nFinished the implementation.",
	} {
		out := transcript.Project([]api.Message{{Role: api.MessageRoleAssistant, Content: raw}})
		got := out[len(out)-1]
		if got.Content != "Finished the implementation." || strings.Contains(got.Content, "⟦D") {
			t.Fatalf("parroted prefix leaked into final reply: %q", got.Content)
		}
	}
}
