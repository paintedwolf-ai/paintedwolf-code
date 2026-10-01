package compaction

import (
	"sort"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/pkg/api"
)

// ChunkKind classifies oversized content for compaction prompts.
type ChunkKind string

const (
	ChunkKindUserPaste       ChunkKind = "user_paste"
	ChunkKindToolResult      ChunkKind = "tool_result"
	ChunkKindAssistantOutput ChunkKind = "assistant_output"
	ChunkKindUnknown         ChunkKind = "unknown"
)

// ContentChunk is one oversized message block eligible for compaction.
type ContentChunk struct {
	MessageIndex int
	MessageID    string
	Tokens       int
	Kind         ChunkKind
	// ToolName resolves through the producing tool-call ID.
	ToolName   string
	PromptTail bool // prune-protected tail tool result; relaxed min-savings
	// HostDataDir is ~/.config/paintedwolf/projects/{id} for tool-output spills. Empty skips spill.
	HostDataDir       string
	MaxToolSpillBytes int
	// EvidenceHandles link the compacted banner to the full observation.
	EvidenceHandles []string
	// Class is the ClassifyMessageDiet result for this chunk (required for routing).
	Class MessageDietClass
}

// FindOversizedChunks returns candidates sorted by token weight descending.
func FindOversizedChunks(messages []ContextMessage, cfg CompactionConfig, binding *evidence.Binding) []ContentChunk {
	if len(messages) == 0 {
		return nil
	}
	threshold := cfg.ChunkTokenThreshold
	if threshold <= 0 {
		threshold = DefaultCompactionConfig().ChunkTokenThreshold
	}

	protectedStart := len(messages)
	if keep := cfg.KeepRecentMessages; keep > 0 && keep < len(messages) {
		protectedStart = len(messages) - keep
	}

	names := toolNamesByCallID(messages)
	var out []ContentChunk
	for i, m := range messages {
		if i >= protectedStart {
			continue
		}
		if m.CompactionCheckpoint {
			continue
		}
		if m.CompactedChunk != nil && m.CompactedChunk.CompactedTokens < m.CompactedChunk.OriginalTokens {
			continue
		}
		ensureToolName(&messages[i], names)
		class := ClassifyMessageDiet(ClassifyMessageDietInput{Index: i, Messages: messages, Binding: binding})
		if class.SkipOversizedChunk(m.Content) {
			continue
		}
		tokens := tokenest.EstimateDefault(m.Content) + PerceiveImageTokenCount(m, true)
		if tokens < threshold {
			continue
		}
		out = append(out, ContentChunk{
			MessageIndex: i,
			MessageID:    m.ID,
			Tokens:       tokens,
			Kind:         classifyChunk(m),
			ToolName:     messages[i].ToolName,
			Class:        class,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Tokens > out[j].Tokens
	})
	return out
}

// toolNamesByCallID resolves tool identity across assistant and result rows.
func toolNamesByCallID(messages []ContextMessage) map[string]string {
	out := make(map[string]string)
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && tc.Name != "" {
				out[tc.ID] = tc.Name
			}
		}
	}
	return out
}

func ensureToolName(m *ContextMessage, names map[string]string) {
	if m == nil {
		return
	}
	if m.ToolName != "" {
		return
	}
	if name := names[m.ToolCallID]; name != "" {
		m.ToolName = name
	}
}

// FindPromptTailToolChunks returns tool-result messages in the prune-protected transcript
// tail that should be spill-summarized before the next coordinator iteration.
func FindPromptTailToolChunks(messages []ContextMessage, cfg CompactionConfig, binding *evidence.Binding) []ContentChunk {
	if len(messages) == 0 {
		return nil
	}
	defaults := DefaultCompactionConfig()
	tail := cfg.PruneProtectTailMessages
	if tail <= 0 {
		tail = defaults.PruneProtectTailMessages
	}
	tailStart := len(messages) - tail
	if tailStart < 0 {
		tailStart = 0
	}
	perMsgThreshold := cfg.ChunkProtectedToolTokenThreshold
	if perMsgThreshold <= 0 {
		perMsgThreshold = defaults.ChunkProtectedToolTokenThreshold
	}
	budget := cfg.ChunkProtectedTailTokenBudget
	if budget <= 0 {
		budget = defaults.ChunkProtectedTailTokenBudget
	}
	target := cfg.ChunkTargetTokens
	if target <= 0 {
		target = defaults.ChunkTargetTokens
	}

	names := toolNamesByCallID(messages)
	type tailTool struct {
		index  int
		tokens int
		class  MessageDietClass
	}
	var tools []tailTool
	total := 0
	for i := tailStart; i < len(messages); i++ {
		m := messages[i]
		if api.MessageRole(m.Role) != api.MessageRoleTool {
			continue
		}
		if m.CompactedChunk != nil && m.CompactedChunk.CompactedTokens < m.CompactedChunk.OriginalTokens {
			continue
		}
		ensureToolName(&messages[i], names)
		class := ClassifyMessageDiet(ClassifyMessageDietInput{Index: i, Messages: messages, Binding: binding})
		if class.SkipOversizedChunk(m.Content) {
			continue
		}
		tok := tokenest.EstimateDefault(m.Content)
		tools = append(tools, tailTool{index: i, tokens: tok, class: class})
		total += tok
	}
	if len(tools) == 0 {
		return nil
	}

	selected := make(map[int]tailTool) // index -> entry
	for _, tt := range tools {
		if tt.tokens >= perMsgThreshold {
			selected[tt.index] = tt
		}
	}
	if total > budget {
		sorted := append([]tailTool(nil), tools...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].tokens > sorted[j].tokens })
		running := total
		for _, tt := range sorted {
			if running <= budget {
				break
			}
			if _, ok := selected[tt.index]; ok {
				savings := tt.tokens - target
				if savings < 0 {
					savings = 0
				}
				running -= savings
				continue
			}
			selected[tt.index] = tt
			savings := tt.tokens - target
			if savings < 0 {
				savings = 0
			}
			running -= savings
		}
	}
	if len(selected) == 0 {
		return nil
	}

	out := make([]ContentChunk, 0, len(selected))
	for idx, tt := range selected {
		m := messages[idx]
		out = append(out, ContentChunk{
			MessageIndex: idx,
			MessageID:    m.ID,
			Tokens:       tt.tokens,
			Kind:         classifyChunk(m),
			ToolName:     messages[idx].ToolName,
			PromptTail:   true,
			Class:        tt.class,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Tokens > out[j].Tokens
	})
	return out
}

// MergeChunkCandidates deduplicates by message index and prefers prompt-tail entries.
func MergeChunkCandidates(parts ...[]ContentChunk) []ContentChunk {
	byIndex := make(map[int]ContentChunk)
	for _, list := range parts {
		for _, ch := range list {
			prev, ok := byIndex[ch.MessageIndex]
			if !ok || ch.PromptTail || ch.Tokens > prev.Tokens {
				byIndex[ch.MessageIndex] = ch
			}
		}
	}
	out := make([]ContentChunk, 0, len(byIndex))
	for _, ch := range byIndex {
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens == out[j].Tokens {
			return out[i].MessageIndex < out[j].MessageIndex
		}
		return out[i].Tokens > out[j].Tokens
	})
	return out
}

func classifyChunk(m ContextMessage) ChunkKind {
	switch api.MessageRole(m.Role) {
	case api.MessageRoleTool:
		return ChunkKindToolResult
	case api.MessageRoleUser:
		lines := toolkit.CountLines(m.Content)
		if lines > 50 {
			return ChunkKindUserPaste
		}
		avg := 0
		if lines > 0 {
			avg = len(m.Content) / lines
		}
		if avg > 120 {
			return ChunkKindUserPaste
		}
		return ChunkKindUnknown
	case api.MessageRoleAssistant:
		if tokenest.EstimateDefault(m.Content) >= DefaultCompactionConfig().ChunkTokenThreshold {
			return ChunkKindAssistantOutput
		}
		return ChunkKindUnknown
	default:
		return ChunkKindUnknown
	}
}
