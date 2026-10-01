package openaicompat

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// Usage is the chat usage block, including provider cache extensions.
type Usage struct {
	PromptTokens             *int           `json:"prompt_tokens"`
	CompletionTokens         *int           `json:"completion_tokens"`
	PromptTokensDetails      *PromptDetails `json:"prompt_tokens_details"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens"`
}

type PromptDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

func NormalizeUsage(u *Usage) modelcall.TokenUsage {
	if u == nil {
		return modelcall.TokenUsage{}
	}
	out := modelcall.TokenUsage{
		Present:                  u.PromptTokens != nil || u.CompletionTokens != nil,
		Incomplete:               u.PromptTokens == nil || u.CompletionTokens == nil,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
	}
	if u.PromptTokens != nil {
		out.PromptTokens = *u.PromptTokens
	}
	if u.CompletionTokens != nil {
		out.CompletionTokens = *u.CompletionTokens
	}
	if u.PromptTokensDetails != nil {
		out.CacheReadInputTokens = u.PromptTokensDetails.CachedTokens
		// These are alternate representations of the same write bucket.
		out.CacheCreationInputTokens = max(out.CacheCreationInputTokens, u.PromptTokensDetails.CacheWriteTokens)
	}
	return out
}
