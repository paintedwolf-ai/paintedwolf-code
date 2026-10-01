package openaicompat

import (
	"encoding/json"
	"testing"
)

func TestReasoningDetailAccumulatorMergesByIndex(t *testing.T) {
	acc := newReasoningDetailAccumulator()
	acc.merge([]reasoningDetailDelta{
		{Type: "reasoning.text", Text: "Step 1...", Index: 0, Format: "moonshot-v1"},
		{Type: "reasoning.text", Text: "Other", Index: 1},
	})
	// A later chunk on the same index continues that block, and the signature
	// only lands on the closing chunk.
	acc.merge([]reasoningDetailDelta{
		{Text: " Step 2...", Index: 0, Signature: "sig"},
	})

	details := acc.details()
	if len(details) != 2 {
		t.Fatalf("details = %d want 2 blocks", len(details))
	}
	var first reasoningDetailDelta
	if err := json.Unmarshal(details[0], &first); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if first.Text != "Step 1... Step 2..." {
		t.Fatalf("text = %q want the concatenation", first.Text)
	}
	if first.Signature != "sig" {
		t.Fatalf("signature = %q — a later empty chunk overwrote it", first.Signature)
	}
	if first.Type != "reasoning.text" || first.Format != "moonshot-v1" {
		t.Fatalf("identity fields lost: %+v", first)
	}
	if got := acc.reasoningText(); got != "Step 1... Step 2...Other" {
		t.Fatalf("reasoningText = %q", got)
	}
}

// Blocks arrive out of order across chunks; index restores the sequence the
// signature was computed over.
func TestReasoningDetailAccumulatorOrdersByIndex(t *testing.T) {
	acc := newReasoningDetailAccumulator()
	acc.merge([]reasoningDetailDelta{{Text: "second", Index: 1}})
	acc.merge([]reasoningDetailDelta{{Text: "first", Index: 0}})
	if got := acc.reasoningText(); got != "firstsecond" {
		t.Fatalf("reasoningText = %q want index order", got)
	}
}
