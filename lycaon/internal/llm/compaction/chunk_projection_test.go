package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/pkg/api"
)

type chunkMemoFixture map[string]messageview.ChunkProjection

func (m chunkMemoFixture) GetChunkProjection(_ context.Context, sessionID, messageID string) (messageview.ChunkProjection, bool, error) {
	p, ok := m[sessionID+"/"+messageID]
	return p, ok, nil
}
func (m chunkMemoFixture) PutChunkProjection(_ context.Context, sessionID, messageID string, p messageview.ChunkProjection) error {
	m[sessionID+"/"+messageID] = p
	return nil
}

type chunkSummaryFixture struct {
	calls int
	text  string
}

func (s *chunkSummaryFixture) Summarize(context.Context, string, string, int) (string, error) {
	s.calls++
	return s.text, nil
}

func TestChunkProjectionReusesExactRevisionAndReplacesParts(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.KeepRecentMessages = 1
	cfg.ChunkTargetTokens = 32
	cfg.ChunkMinSavingsTokens = 100
	summary := &chunkSummaryFixture{text: "Useful facts retained."}
	c := NewSimpleCompactor(cfg, summary)
	info := SessionInfo{ID: "session", ChunkProjections: chunkMemoFixture{}}
	content := strings.Repeat("source material ", 2000)
	canonical := []api.Message{{ID: "source", Role: api.MessageRoleUser, Content: content,
		ContentParts: []api.MessageContentPart{{Content: content, Origin: api.MessageOriginAttachment,
			Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted}}},
		{ID: "tail", Role: api.MessageRoleAssistant, Content: "recent"}}
	input := ContextMessagesFromAPI(canonical)
	for range 2 {
		out, count := c.CompactOversizedChunksOnly(t.Context(), info, input)
		if count != 1 || out[0].Content == content || out[0].ContentParts[0].Content != out[0].Content {
			t.Fatal("projection did not replace every text surface")
		}
		if out[0].Origin != api.MessageOriginModel || out[0].Authority != api.ContentAuthorityNone || out[0].TrustTier != api.ContentTrustTierUntrusted {
			t.Fatalf("projection provenance = %+v", out[0])
		}
	}
	if summary.calls != 1 {
		t.Fatalf("unchanged source summarized %d times", summary.calls)
	}
	if input[0].ContentParts[0].Content != content || canonical[0].Content != content {
		t.Fatal("canonical input mutated")
	}
	input[0].Content += "changed source"
	c.CompactOversizedChunksOnly(t.Context(), info, input)
	if summary.calls != 2 {
		t.Fatal("changed source reused stale summary")
	}
	input[0].ContextPinned = true
	c.CompactOversizedChunksOnly(t.Context(), info, input)
	if summary.calls != 2 {
		t.Fatal("pinned source was summarized")
	}
}

func TestUnproductiveChunkSummaryIsNotPurchasedAgain(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.KeepRecentMessages = 1
	content := strings.Repeat("unchanged material ", 2000)
	summary := &chunkSummaryFixture{text: content}
	c := NewSimpleCompactor(cfg, summary)
	info := SessionInfo{ID: "session", ChunkProjections: chunkMemoFixture{}}
	input := []ContextMessage{{ID: "source", Role: "user", Content: content}, {ID: "tail", Role: "assistant", Content: "recent"}}
	for range 3 {
		out, count := c.CompactOversizedChunksOnly(t.Context(), info, input)
		if count != 0 || out[0].CompactedChunk != nil || out[0].Content != content {
			t.Fatal("unproductive summary changed projection")
		}
	}
	if summary.calls != 1 {
		t.Fatalf("unproductive source summarized %d times", summary.calls)
	}
}

func TestRememberedNoOpDoesNotStarveLaterChunks(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.KeepRecentMessages = 1
	cfg.ChunkMaxPerPass = 1
	content := strings.Repeat("unchanged material ", 2000)
	summary := &chunkSummaryFixture{text: content}
	c := NewSimpleCompactor(cfg, summary)
	info := SessionInfo{ID: "session", ChunkProjections: chunkMemoFixture{}}
	input := []ContextMessage{{ID: "first", Role: "user", Content: content},
		{ID: "second", Role: "user", Content: content}, {ID: "tail", Role: "assistant", Content: "recent"}}
	c.CompactOversizedChunksOnly(t.Context(), info, input)
	if summary.calls != 1 {
		t.Fatalf("first pass attempted %d summaries", summary.calls)
	}
	summary.text = "Useful reduction."
	out, count := c.CompactOversizedChunksOnly(t.Context(), info, input)
	if summary.calls != 2 || count != 1 || out[1].CompactedChunk == nil {
		t.Fatalf("remembered no-op starved later chunk: calls=%d accepted=%d", summary.calls, count)
	}
}

func TestCompactionRetainsPinsAndCompleteToolExchange(t *testing.T) {
	input := []ContextMessage{{ID: "charter", Role: "user", ContextPinned: true}, {ID: "old", Role: "assistant"},
		{ID: "call", Role: "assistant", ToolCalls: []api.ToolCall{{ID: "tool"}}},
		{ID: "result", Role: "tool", ToolCallID: "tool"}, {ID: "recent", Role: "user"}}
	retained := retainedCompactionMessages(input, 2)
	if len(retained) != 4 || retained[0].ID != "charter" || retained[1].ID != "call" {
		t.Fatalf("retained = %+v", retained)
	}
	removed := removedCompactionMessages(input, retained)
	if len(removed) != 1 || removed[0].ID != "old" {
		t.Fatalf("removed = %+v", removed)
	}
}

func TestChunkProjectionUpdatesToolResultBody(t *testing.T) {
	canonical := []api.Message{{ID: "tool", Role: api.MessageRoleTool, Content: "original", ToolResult: &api.ToolResult{Content: "original", ToolCallID: "call"}}}
	msg := applyChunkProjection(ContextMessageFromAPI(canonical[0]), messageview.ChunkProjection{
		Content: "residue", Meta: &api.CompactedChunkMeta{Strategy: "spill_index"}})
	out := ContextMessagesToAPI([]ContextMessage{msg}, canonical)
	if out[0].ToolResult.Content != "residue" || canonical[0].ToolResult.Content != "original" {
		t.Fatal("tool result body disagrees with prompt projection")
	}
}

func TestProtectedChunksRemainIneligibleUnderForcedDiet(t *testing.T) {
	for _, msg := range []ContextMessage{
		{Role: "user", ContextPinned: true},
		{Role: "assistant", DietStampSource: DietStampSourceWorkerEnvelope},
		{Role: "user", CompactionCheckpoint: true},
	} {
		class := ClassifyMessageDiet(ClassifyMessageDietInput{Index: 0, Messages: []ContextMessage{msg}, ForceEligible: true})
		if class.Eligibility != DietEligibilityPinned || !class.SkipOversizedChunk("large body") {
			t.Fatalf("protection lost under forced diet: %+v", class)
		}
	}
}

func TestContextProjectionRequiresIdentityForRehydration(t *testing.T) {
	original := []api.Message{{Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{ToolCallID: "call", Content: "canonical tool output"}}}
	projected := []ContextMessage{{Role: "assistant", Content: "generated summary"}}
	out := ContextMessagesToAPI(projected, original)
	if len(out) != 1 || out[0].ToolResult != nil || out[0].Content != projected[0].Content {
		t.Fatalf("anonymous projection inherited unrelated fields: %+v", out)
	}
}
