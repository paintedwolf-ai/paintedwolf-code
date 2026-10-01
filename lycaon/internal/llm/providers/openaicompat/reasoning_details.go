package openaicompat

import (
	"encoding/json"
	"sort"
	"strings"
)

// A `reasoning_details` block may be a summary, a signed trace, or an encrypted
// payload, so the host keeps the blocks as bytes and replays them verbatim.

// reasoningDetailDelta is one streamed reasoning block. Scalar identity fields
// arrive once and repeat; text/summary/data arrive in pieces and concatenate.
// Index orders the blocks and is the merge key: two chunks carrying index 0 are
// the same block continuing, not two blocks.
type reasoningDetailDelta struct {
	Type      string `json:"type,omitempty"`
	Text      string `json:"text,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Data      string `json:"data,omitempty"`
	Signature string `json:"signature,omitempty"`
	ID        string `json:"id,omitempty"`
	Format    string `json:"format,omitempty"`
	Index     int    `json:"index,omitempty"`
}

// reasoningDetailAccumulator merges streamed blocks back into the array the
// provider would have returned non-streaming.
type reasoningDetailAccumulator struct {
	blocks map[int]*reasoningDetailDelta
}

func newReasoningDetailAccumulator() *reasoningDetailAccumulator {
	return &reasoningDetailAccumulator{blocks: map[int]*reasoningDetailDelta{}}
}

func (a *reasoningDetailAccumulator) merge(deltas []reasoningDetailDelta) {
	for _, d := range deltas {
		block, ok := a.blocks[d.Index]
		if !ok {
			clone := d
			a.blocks[d.Index] = &clone
			continue
		}
		block.Text += d.Text
		block.Summary += d.Summary
		block.Data += d.Data
		// Identity fields are last-write-wins: a provider that only stamps the
		// signature on the closing chunk must not have it overwritten by the
		// empty string every earlier chunk carried.
		if d.Type != "" {
			block.Type = d.Type
		}
		if d.Signature != "" {
			block.Signature = d.Signature
		}
		if d.ID != "" {
			block.ID = d.ID
		}
		if d.Format != "" {
			block.Format = d.Format
		}
	}
}

func (a *reasoningDetailAccumulator) empty() bool { return len(a.blocks) == 0 }

// details renders the merged blocks in index order. Marshal failure drops the
// block rather than the turn: a trace that cannot be re-encoded is one the
// provider would reject on replay anyway.
func (a *reasoningDetailAccumulator) details() []json.RawMessage {
	if len(a.blocks) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(a.blocks))
	for idx := range a.blocks {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	out := make([]json.RawMessage, 0, len(indexes))
	for _, idx := range indexes {
		raw, err := json.Marshal(a.blocks[idx])
		if err != nil {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// reasoningText renders the merged blocks as plaintext, for providers that
// stream structured blocks without the flat `reasoning` mirror. Encrypted
// blocks contribute nothing — their payload is not text.
func (a *reasoningDetailAccumulator) reasoningText() string {
	if len(a.blocks) == 0 {
		return ""
	}
	indexes := make([]int, 0, len(a.blocks))
	for idx := range a.blocks {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	var b strings.Builder
	for _, idx := range indexes {
		block := a.blocks[idx]
		b.WriteString(block.Text)
		b.WriteString(block.Summary)
	}
	return b.String()
}
