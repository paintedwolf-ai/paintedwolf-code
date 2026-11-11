package llm

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

func TestReasoningPresenceSurvivesSplitMarkers(t *testing.T) {
	for split := 0; split <= len("<think>"); split++ {
		tag := "<think>"
		visible, reasoning := readAll(thinkMarkers,
			modelcall.StreamChunk{Content: tag[:split], ReasoningPresent: true},
			content(tag[split:]+"\nAnswer"),
		)
		if visible != "Answer" || reasoning != "" {
			t.Fatalf("split %d: visible=%q reasoning=%q", split, visible, reasoning)
		}
	}
}

func TestReasoningPresenceDoesNotHideUnframedAnswer(t *testing.T) {
	const answer = "Internal working note is quoted text.</think>"
	visible, reasoning := readAll(thinkMarkers, modelcall.StreamChunk{Content: answer, ReasoningPresent: true})
	if visible != answer || reasoning != "" {
		t.Fatalf("visible=%q reasoning=%q", visible, reasoning)
	}
}

func TestStructuredReasoningSettlesPendingOpeningTag(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers, content("<think>"), modelcall.StreamChunk{
		Content: "Answer", ReasoningDetails: []json.RawMessage{json.RawMessage(`{"type":"reasoning.encrypted","data":"opaque"}`)},
	})
	if visible != "Answer" || reasoning != "" {
		t.Fatalf("visible=%q reasoning=%q", visible, reasoning)
	}
}

func TestReasoningPresenceResetsBetweenAttempts(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers,
		modelcall.StreamChunk{ReasoningPresent: true},
		modelcall.StreamChunk{ResetReasoning: true},
		content("<think>private</think>Answer"),
	)
	if visible != "Answer" || reasoning != "private" {
		t.Fatalf("visible=%q reasoning=%q", visible, reasoning)
	}
}
