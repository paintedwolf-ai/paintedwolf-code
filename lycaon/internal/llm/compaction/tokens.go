package compaction

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tokenest"
)

// BudgetRemaining returns remaining tokens for a scope budget.
func BudgetRemaining(max, used int) int {
	if max <= 0 {
		return max
	}
	if used >= max {
		return 0
	}
	return max - used
}

// PromptTokenCalibration scales transcript estimates with observed usage. The
// ratio follows transcript shrinkage after compaction and avoids retriggering.
type PromptTokenCalibration struct {
	// ReportedPromptTokens is the provider's prompt_tokens for the last completion.
	ReportedPromptTokens int
	// TranscriptEstimate is the host estimate for that same request.
	TranscriptEstimate int
}

// Calibrated reports whether an observation is available to scale from.
func (c PromptTokenCalibration) Calibrated() bool {
	return c.ReportedPromptTokens > 0 && c.TranscriptEstimate > 0
}

// ProjectBilled returns what the provider is expected to bill for a transcript of
// the given estimated size. Without an observation it falls back to the additive
// cold-start reserve.
//
// The ratio floors at 1 so an estimate above the billed count cannot raise the
// effective ceiling past the model window.
func (c PromptTokenCalibration) ProjectBilled(transcriptEstimate, coldStartOverhead int) int {
	if transcriptEstimate < 0 {
		transcriptEstimate = 0
	}
	if !c.Calibrated() {
		if coldStartOverhead <= 0 {
			return transcriptEstimate
		}
		return transcriptEstimate + coldStartOverhead
	}
	if c.ReportedPromptTokens <= c.TranscriptEstimate {
		return transcriptEstimate
	}
	scaled := int64(transcriptEstimate) * int64(c.ReportedPromptTokens) / int64(c.TranscriptEstimate)
	return int(scaled)
}

// EstimateBudget returns the largest transcript estimate whose projected billed
// size still fits billedCeiling. The result is quantized down to
// fitOverheadQuantum so ratio jitter does not move the fitted prefix start and
// invalidate the provider prompt cache.
func (c PromptTokenCalibration) EstimateBudget(billedCeiling, coldStartOverhead int) int {
	if billedCeiling <= 0 {
		return 0
	}
	var budget int
	switch {
	case !c.Calibrated():
		budget = billedCeiling - coldStartOverhead
	case c.ReportedPromptTokens <= c.TranscriptEstimate:
		budget = billedCeiling
	default:
		budget = int(int64(billedCeiling) * int64(c.TranscriptEstimate) / int64(c.ReportedPromptTokens))
	}
	if budget < 0 {
		budget = 0
	}
	return budget / fitOverheadQuantum * fitOverheadQuantum
}

// defaultColdStartFitOverheadTokens reserves static-stack room when no provider
// observation is available yet (tools + system injects).
const defaultColdStartFitOverheadTokens = 15000

// fitOverheadQuantum buckets the observed overhead, rounding up, so the fit
// budget and its front-drop count hold still across calls; an unbucketed value
// moves with every prompt_tokens report and breaks the provider prompt cache.
const fitOverheadQuantum = 2048

// coldStartMaxWindowPct caps the cold-start reserve as a share of the model
// window, so a small window is not over the trigger while still empty.
const coldStartMaxWindowPct = 25

// ColdStartOverhead clamps a static-stack reserve to a share of this config's
// model window. An unknown window leaves the reserve unchanged.
func (c CompactionConfig) ColdStartOverhead(reserve int) int {
	if reserve <= 0 || c.ModelContextWindow <= 0 {
		return reserve
	}
	if cap := c.ModelContextWindow * coldStartMaxWindowPct / 100; reserve > cap {
		return cap
	}
	return reserve
}

// ResolveColdStartOverhead returns the reserve used until a provider reports
// prompt_tokens: the tool schema estimate, never below the default reserve.
func ResolveColdStartOverhead(toolsJSON string) int {
	cold := defaultColdStartFitOverheadTokens
	if toolsJSON == "" {
		return cold
	}
	if est := tokenest.EstimateDefault(toolsJSON); est > cold {
		return est
	}
	return cold
}

// FitMaxTokens returns the message-fit budget in transcript-estimate units,
// never below TargetTokens or 1.
func FitMaxTokens(cfg CompactionConfig, cal PromptTokenCalibration, coldStartOverhead int) int {
	max := cal.EstimateBudget(cfg.HardCeilingTokens, coldStartOverhead)
	if max < cfg.TargetTokens {
		max = cfg.TargetTokens
	}
	if max < 1 {
		max = 1
	}
	return max
}

// EstimateMessagesTokens sums message contents and tool-call args; edit and
// write args carry whole file bodies.
func EstimateMessagesTokens(messages []ContextMessage) int {
	return EstimateMessagesTokensWithVision(messages, true)
}

// EstimateMessagesTokensWithVision includes image estimates when images are
// attached, charging tool images only inside the perception window.
func EstimateMessagesTokensWithVision(messages []ContextMessage, attachImages bool) int {
	total := 0
	windowStart := toolImageWindowStart(messages)
	toolImages := 0
	for _, m := range messages {
		total += tokenest.EstimateDefault(m.Content)
		if attachImages {
			total += m.AttachmentImageTokens
			if m.ToolImageTokens > 0 {
				if toolImages >= windowStart {
					total += m.ToolImageTokens
				}
				toolImages++
			}
		}
		for _, tc := range m.ToolCalls {
			total += tokenest.EstimateDefault(tc.Name)
			if len(tc.Args) > 0 {
				if raw, err := json.Marshal(tc.Args); err == nil {
					total += tokenest.EstimateDefault(string(raw))
				}
			}
		}
	}
	return total
}

// toolImageWindowStart is how many of the oldest tool images fall outside the
// perception window and so cost nothing.
func toolImageWindowStart(messages []ContextMessage) int {
	n := 0
	for _, m := range messages {
		if m.ToolImageTokens > 0 {
			n++
		}
	}
	return providerwire.CurrentPerceptionWindow().Dropped(n)
}

// PerceiveImageTokenCount returns one message's image charge when images are
// attached, without the window that only a whole transcript determines.
func PerceiveImageTokenCount(m ContextMessage, attachImages bool) int {
	if !attachImages {
		return 0
	}
	return m.ToolImageTokens + m.AttachmentImageTokens
}
