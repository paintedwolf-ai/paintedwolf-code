package llm

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// Providers report cumulative snapshots within one attempt. Retries add billable
// attempts, so consumers must see their combined usage, including failed turns.
type responseRetryUsage struct {
	completed modelcall.TokenUsage
	current   modelcall.TokenUsage
}

func (u *responseRetryUsage) observe(usage modelcall.TokenUsage) {
	if usage.Reported() {
		u.current = usage
	}
}

func (u *responseRetryUsage) total() modelcall.TokenUsage {
	a, b := u.completed, u.current
	return modelcall.TokenUsage{
		Present:                    a.Reported() || b.Reported(),
		Incomplete:                 a.Incomplete || b.Incomplete,
		PromptTokens:               a.PromptTokens + b.PromptTokens,
		CompletionTokens:           a.CompletionTokens + b.CompletionTokens,
		CacheReadInputTokens:       a.CacheReadInputTokens + b.CacheReadInputTokens,
		CacheCreationInputTokens:   a.CacheCreationInputTokens + b.CacheCreationInputTokens,
		CacheCreation1HInputTokens: a.CacheCreation1HInputTokens + b.CacheCreation1HInputTokens,
	}
}

func (u *responseRetryUsage) finishAttempt() {
	u.completed = u.total()
	if !u.current.Reported() {
		u.completed.Incomplete = true
	}
	u.current = modelcall.TokenUsage{}
}
